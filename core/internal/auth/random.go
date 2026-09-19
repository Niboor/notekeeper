package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"strings"
)

// RandomToken returns n random bytes as unpadded base64url. 24 bytes give a 192-bit token.
func RandomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error()) // no safe way to continue without entropy
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// HashToken returns the SHA-256 of a high-entropy random token. Such tokens need no slow hash
// (docs/design/03-auth.md section 1).
func HashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewPairingCode returns a random code of 8 Crockford base32 characters (40 bits) formatted
// XXXX-XXXX (AUTH-B3).
func NewPairingCode() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	out := make([]byte, 0, 9)
	for i, v := range b {
		if i == 4 {
			out = append(out, '-')
		}
		out = append(out, crockford[int(v)&31])
	}
	return string(out)
}

// NormalisePairingCode upper-cases a user-typed code, maps the look-alike letters Crockford
// treats as digits, and removes separators, so "abcd 1234" and "ABCD-1234" are the same code.
// It returns "" for anything that cannot be a code.
func NormalisePairingCode(s string) string {
	var b strings.Builder
	for _, c := range strings.ToUpper(s) {
		switch c {
		case '-', ' ':
			continue
		case 'O':
			c = '0'
		case 'I', 'L':
			c = '1'
		}
		if !strings.ContainsRune(crockford, c) {
			return ""
		}
		b.WriteRune(c)
	}
	if b.Len() != 8 {
		return ""
	}
	n := b.String()
	return n[:4] + "-" + n[4:]
}
