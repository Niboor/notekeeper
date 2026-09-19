// Package auth holds Notekeeper's credential primitives: password hashing and policy, MAC-derived
// session tokens, and random secrets (docs/design/03-auth.md sections 1 to 3).
package auth

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	_ "embed" // the common-password list
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Params are the argon2id cost parameters (SEC-BASE-1): memory in KiB, iterations, parallelism.
type Params struct {
	Memory  uint32
	Time    uint32
	Threads uint8
}

// DefaultParams are 64 MiB, 3 iterations, 2 lanes.
var DefaultParams = Params{Memory: 64 * 1024, Time: 3, Threads: 2}

// MinPasswordLength is the shortest accepted password (SEC-AUTH-5).
const MinPasswordLength = 10

// ErrPasswordPolicy is returned for a password that is too short or too common.
var ErrPasswordPolicy = errors.New("password does not meet the policy")

//go:embed commonpasswords.txt
var commonPasswordsRaw string

var commonPasswords = func() map[string]struct{} {
	m := make(map[string]struct{}, 10000)
	sc := bufio.NewScanner(strings.NewReader(commonPasswordsRaw))
	for sc.Scan() {
		if p := strings.TrimSpace(sc.Text()); p != "" {
			m[p] = struct{}{}
		}
	}
	return m
}()

// CheckPolicy enforces the password policy: at least 10 characters and not on the embedded list
// of common passwords, compared case-insensitively (SEC-AUTH-5). Nothing is sent anywhere.
func CheckPolicy(password string) error {
	if utf8.RuneCountInString(password) < MinPasswordLength {
		return fmt.Errorf("%w: shorter than %d characters", ErrPasswordPolicy, MinPasswordLength)
	}
	if _, bad := commonPasswords[strings.ToLower(password)]; bad {
		return fmt.Errorf("%w: too common", ErrPasswordPolicy)
	}
	return nil
}

// Hasher hashes and verifies passwords with argon2id. A semaphore bounds the number of
// concurrent hash computations so a burst of logins cannot exhaust memory (SEC-API-4).
type Hasher struct {
	params Params
	sem    chan struct{}
	dummy  string
}

// NewHasher creates a Hasher allowing at most concurrency simultaneous computations.
func NewHasher(p Params, concurrency int) (*Hasher, error) {
	if concurrency < 1 {
		concurrency = 1
	}
	h := &Hasher{params: p, sem: make(chan struct{}, concurrency)}
	dummy, err := h.Hash(context.Background(), "dummy password for equal-work verification")
	if err != nil {
		return nil, err
	}
	h.dummy = dummy
	return h, nil
}

func (h *Hasher) acquire(ctx context.Context) error {
	select {
	case h.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *Hasher) release() { <-h.sem }

// Hash returns the PHC-encoded argon2id hash of password with a fresh random salt.
func (h *Hasher) Hash(ctx context.Context, password string) (string, error) {
	if err := h.acquire(ctx); err != nil {
		return "", err
	}
	defer h.release()
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, h.params.Time, h.params.Memory, h.params.Threads, 32)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version,
		h.params.Memory, h.params.Time, h.params.Threads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// Verify checks password against an encoded hash. needsRehash reports that the stored hash uses
// weaker parameters than the current configuration (SEC-BASE-1).
func (h *Hasher) Verify(ctx context.Context, password, encoded string) (ok, needsRehash bool, err error) {
	var version int
	var p Params
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, false, errors.New("unsupported hash format")
	}
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, false, errors.New("unsupported argon2 version")
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Time, &p.Threads); err != nil {
		return false, false, errors.New("bad argon2 parameters")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, false, err
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, false, err
	}
	if err := h.acquire(ctx); err != nil {
		return false, false, err
	}
	defer h.release()
	got := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, uint32(len(want)))
	ok = subtle.ConstantTimeCompare(got, want) == 1
	needsRehash = p.Memory < h.params.Memory || p.Time < h.params.Time || p.Threads < h.params.Threads
	return ok, needsRehash, nil
}

// VerifyDummy performs the same work as a real verification against a fixed hash and always
// fails. Used when the account does not exist or cannot log in, so timing reveals nothing (SEC-AUTH-3).
func (h *Hasher) VerifyDummy(ctx context.Context, password string) {
	_, _, _ = h.Verify(ctx, password, h.dummy)
}
