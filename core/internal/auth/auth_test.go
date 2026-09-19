package auth

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func testKeyring(t *testing.T, ids ...string) *Keyring {
	t.Helper()
	var parts []string
	for _, id := range ids {
		parts = append(parts, id+":"+base64.StdEncoding.EncodeToString([]byte(strings.Repeat(id, 32))))
	}
	k, err := ParseKeyring(strings.Join(parts, ","))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestPasswordPolicy(t *testing.T) {
	cases := []struct {
		pw string
		ok bool
	}{
		{"short", false},
		{"123456789", false},   // 9 characters
		{"password123", false}, // on the common list
		{"PASSWORD123", false}, // list is case-insensitive
		{"correct horse battery", true},
		{"ünïcödé-pässwörd", true},
		{"éééééééééé", true}, // ten runes even though more bytes
	}
	for _, c := range cases {
		err := CheckPolicy(c.pw)
		if (err == nil) != c.ok {
			t.Errorf("CheckPolicy(%q) = %v, want ok=%v", c.pw, err, c.ok)
		}
	}
}

func TestPasswordHashVerifyAndRehash(t *testing.T) {
	ctx := context.Background()
	weak, err := NewHasher(Params{Memory: 8, Time: 1, Threads: 1}, 2)
	if err != nil {
		t.Fatal(err)
	}
	strong, err := NewHasher(Params{Memory: 16, Time: 1, Threads: 1}, 2)
	if err != nil {
		t.Fatal(err)
	}
	enc, err := weak.Hash(ctx, "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if ok, rehash, err := weak.Verify(ctx, "correct horse battery", enc); err != nil || !ok || rehash {
		t.Fatalf("verify: ok=%v rehash=%v err=%v", ok, rehash, err)
	}
	if ok, _, _ := weak.Verify(ctx, "wrong", enc); ok {
		t.Fatal("wrong password accepted")
	}
	if ok, rehash, err := strong.Verify(ctx, "correct horse battery", enc); err != nil || !ok || !rehash {
		t.Fatalf("hash with old parameters must verify and ask for a rehash: ok=%v rehash=%v err=%v", ok, rehash, err)
	}
	other, _ := weak.Hash(ctx, "correct horse battery")
	if other == enc {
		t.Fatal("salts must differ")
	}
	if _, _, err := weak.Verify(ctx, "x", "$bcrypt$nope"); err == nil {
		t.Fatal("unsupported format must error")
	}
}

func TestHasherConcurrencyBound(t *testing.T) {
	h, err := NewHasher(Params{Memory: 8, Time: 1, Threads: 1}, 1)
	if err != nil {
		t.Fatal(err)
	}
	h.sem <- struct{}{} // occupy the only slot
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := h.Hash(ctx, "x"); err == nil {
		t.Fatal("hash must wait for a slot and give up when the context ends")
	}
}

func TestAccessToken(t *testing.T) {
	k := testKeyring(t, "k1")
	sid := uuid.New()
	now := time.Unix(1_800_000_000, 0)
	tok := k.Access(sid, now.Add(15*time.Minute))
	if got, err := k.ParseAccess(tok, now); err != nil || got != sid {
		t.Fatalf("parse: %v %v", got, err)
	}
	if _, err := k.ParseAccess(tok, now.Add(15*time.Minute)); err == nil {
		t.Fatal("expired token accepted")
	}
	if _, err := k.ParseAccess(tok+"x", now); err == nil {
		t.Fatal("tampered token accepted")
	}
	// A refresh token must never be usable as an access token, and vice versa.
	if _, err := k.ParseAccess(k.Refresh(sid, 1), now); err == nil {
		t.Fatal("refresh token accepted as access token")
	}
	if _, _, err := k.ParseRefresh(tok); err == nil {
		t.Fatal("access token accepted as refresh token")
	}
}

func TestRefreshTokenDeterministicPerGeneration(t *testing.T) {
	k := testKeyring(t, "k1")
	sid := uuid.New()
	a, b := k.Refresh(sid, 3), k.Refresh(sid, 3)
	if a != b {
		t.Fatal("the same generation must derive the same token (grace window relies on it)")
	}
	if a == k.Refresh(sid, 4) {
		t.Fatal("generations must differ")
	}
	got, gen, err := k.ParseRefresh(a)
	if err != nil || got != sid || gen != 3 {
		t.Fatalf("parse: %v %v %v", got, gen, err)
	}
	if k.Refresh(uuid.New(), 3) == a {
		t.Fatal("sessions must differ")
	}
}

func TestKeyRotation(t *testing.T) {
	old := testKeyring(t, "a")
	rotated := testKeyring(t, "b", "a") // b signs, a still verifies
	sid := uuid.New()
	now := time.Unix(1_800_000_000, 0)
	tok := old.Access(sid, now.Add(time.Minute))
	if _, err := rotated.ParseAccess(tok, now); err != nil {
		t.Fatalf("token signed by the previous key must verify: %v", err)
	}
	if _, err := testKeyring(t, "b").ParseAccess(tok, now); err == nil {
		t.Fatal("token from a removed key accepted")
	}
	if strings.Contains(rotated.Access(sid, now.Add(time.Minute)), tok) {
		t.Fatal("unexpected")
	}
}

func TestParseKeyringRejectsWeakKeys(t *testing.T) {
	for _, spec := range []string{"", "nokid", "k:" + base64.StdEncoding.EncodeToString([]byte("short")), "k:%%%"} {
		if _, err := ParseKeyring(spec); err == nil {
			t.Errorf("ParseKeyring(%q) should fail", spec)
		}
	}
}

func TestPairingCodes(t *testing.T) {
	for range 50 {
		c := NewPairingCode()
		if len(c) != 9 || c[4] != '-' {
			t.Fatalf("bad code %q", c)
		}
		if NormalisePairingCode(strings.ToLower(c)) != c {
			t.Fatalf("code %q does not normalise to itself", c)
		}
	}
	if got := NormalisePairingCode("abcd o1lI"); got != "ABCD-0111" {
		t.Fatalf("look-alikes: %q", got)
	}
	for _, bad := range []string{"", "ABCD", "ABCD-EFGHJ", "ABCD-EFG!", "UUUU-UUUU"} {
		if NormalisePairingCode(bad) != "" {
			t.Errorf("%q should not normalise", bad)
		}
	}
}

func TestHashToken(t *testing.T) {
	if string(HashToken("a")) == string(HashToken("b")) || len(HashToken("a")) != 32 {
		t.Fatal("hash")
	}
	if a, b := RandomToken(24), RandomToken(24); a == b || len(a) != 32 {
		t.Fatalf("random tokens: %q %q", a, b)
	}
}
