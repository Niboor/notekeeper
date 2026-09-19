package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Access and refresh tokens are derived, not stored: a MAC over the session id and a generation
// or expiry, keyed by a server key (docs/design/03-auth.md section 2.1). A database leak alone
// therefore reveals no usable token, and the grace window can recompute the same successor.

const (
	accessPrefix  = "nka."
	refreshPrefix = "nkr."
)

// ErrBadToken is returned for a token that is malformed, forged, signed by an unknown key or expired.
var ErrBadToken = errors.New("invalid token")

type tokenKey struct {
	id  string
	key []byte
}

// Keyring holds the MAC keys. The first signs; all verify, so keys can be rotated.
type Keyring struct{ keys []tokenKey }

// ParseKeyring parses "kid:base64key,kid2:base64key2". Keys must decode to at least 32 bytes.
func ParseKeyring(spec string) (*Keyring, error) {
	var k Keyring
	for _, item := range strings.Split(spec, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		id, enc, ok := strings.Cut(item, ":")
		if !ok || id == "" || len(id) > 32 {
			return nil, errors.New("token key must look like kid:base64")
		}
		raw, err := base64.StdEncoding.DecodeString(enc)
		if err != nil {
			raw, err = base64.RawStdEncoding.DecodeString(enc)
		}
		if err != nil || len(raw) < 32 {
			return nil, fmt.Errorf("token key %q must be at least 32 bytes of base64", id)
		}
		k.keys = append(k.keys, tokenKey{id: id, key: raw})
	}
	if len(k.keys) == 0 {
		return nil, errors.New("no token keys configured")
	}
	return &k, nil
}

func mac(key []byte, purpose byte, session uuid.UUID, n uint64) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte{purpose})
	h.Write(session[:])
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], n)
	h.Write(b[:])
	return h.Sum(nil)
}

func (k *Keyring) build(prefix string, purpose byte, session uuid.UUID, n uint64) string {
	signer := k.keys[0]
	buf := make([]byte, 0, 1+len(signer.id)+16+8+32)
	buf = append(buf, byte(len(signer.id)))
	buf = append(buf, signer.id...)
	buf = append(buf, session[:]...)
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], n)
	buf = append(buf, b[:]...)
	buf = append(buf, mac(signer.key, purpose, session, n)...)
	return prefix + base64.RawURLEncoding.EncodeToString(buf)
}

func (k *Keyring) parse(prefix string, purpose byte, token string) (uuid.UUID, uint64, error) {
	rest, ok := strings.CutPrefix(token, prefix)
	if !ok {
		return uuid.Nil, 0, ErrBadToken
	}
	raw, err := base64.RawURLEncoding.DecodeString(rest)
	if err != nil || len(raw) < 1 {
		return uuid.Nil, 0, ErrBadToken
	}
	idLen := int(raw[0])
	if len(raw) != 1+idLen+16+8+32 {
		return uuid.Nil, 0, ErrBadToken
	}
	kid := string(raw[1 : 1+idLen])
	var session uuid.UUID
	copy(session[:], raw[1+idLen:])
	n := binary.BigEndian.Uint64(raw[1+idLen+16:])
	got := raw[1+idLen+16+8:]
	for _, key := range k.keys {
		if key.id == kid {
			if hmac.Equal(got, mac(key.key, purpose, session, n)) {
				return session, n, nil
			}
			return uuid.Nil, 0, ErrBadToken
		}
	}
	return uuid.Nil, 0, ErrBadToken
}

// Access derives the access token of a session that is valid until exp.
func (k *Keyring) Access(session uuid.UUID, exp time.Time) string {
	return k.build(accessPrefix, 'a', session, uint64(exp.Unix()))
}

// ParseAccess verifies an access token and returns its session id. Expired tokens fail.
func (k *Keyring) ParseAccess(token string, now time.Time) (uuid.UUID, error) {
	session, _, err := k.ParseAccessExpiry(token, now)
	return session, err
}

// ParseAccessExpiry is ParseAccess that also returns when the token lapses.
func (k *Keyring) ParseAccessExpiry(token string, now time.Time) (uuid.UUID, time.Time, error) {
	session, exp, err := k.parse(accessPrefix, 'a', token)
	if err != nil {
		return uuid.Nil, time.Time{}, err
	}
	if now.Unix() >= int64(exp) {
		return uuid.Nil, time.Time{}, ErrBadToken
	}
	return session, time.Unix(int64(exp), 0), nil
}

// Refresh derives the refresh token of a session for a refresh generation.
func (k *Keyring) Refresh(session uuid.UUID, generation int32) string {
	return k.build(refreshPrefix, 'r', session, uint64(generation))
}

// ParseRefresh verifies a refresh token and returns its session id and generation.
func (k *Keyring) ParseRefresh(token string) (uuid.UUID, int32, error) {
	session, gen, err := k.parse(refreshPrefix, 'r', token)
	if err != nil {
		return uuid.Nil, 0, err
	}
	return session, int32(gen), nil
}
