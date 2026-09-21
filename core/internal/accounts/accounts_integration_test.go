//go:build integration

package accounts_test

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/Niboor/notekeeper/core/internal/accounts"
	"github.com/Niboor/notekeeper/core/internal/auth"
	"github.com/Niboor/notekeeper/core/internal/obs"
	"github.com/Niboor/notekeeper/core/internal/store"
	"github.com/Niboor/notekeeper/core/internal/testdb"
)

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

const goodPassword = "correct horse battery staple"

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type env struct {
	svc *accounts.Service
	clk *clock
	db  *testdb.DB
}

func newEnv(t *testing.T) *env {
	t.Helper()
	d := testdb.New(t)
	keys, err := auth.ParseKeyring("k1:" + base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))))
	if err != nil {
		t.Fatal(err)
	}
	hasher, err := auth.NewHasher(auth.Params{Memory: 8, Time: 1, Threads: 1}, 8)
	if err != nil {
		t.Fatal(err)
	}
	clk := &clock{t: time.Now().Truncate(time.Second)}
	svc := accounts.New(store.New(d.App), keys, hasher, accounts.DefaultConfig(), slog.New(slog.DiscardHandler))
	svc.Now = clk.Now
	return &env{svc: svc, clk: clk, db: d}
}

// activeUser creates a user, activates it and returns the login tokens.
func (e *env) activeUser(t *testing.T, name string) (uuid.UUID, accounts.Tokens) {
	t.Helper()
	ctx := context.Background()
	u, err := e.svc.CreateUser(ctx, accounts.Actor{Kind: "system"}, accounts.CreateUserInput{Username: name})
	if err != nil {
		t.Fatal(err)
	}
	link, err := e.svc.IssueActivation(ctx, accounts.Actor{Kind: "system"}, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := e.svc.Activate(ctx, link.Token, goodPassword, accounts.ClientInfo{IP: "10.0.0.1", UserAgent: "Mozilla/5.0 (X11; Linux x86_64) Gecko/20100101 Firefox/130.0"})
	if err != nil {
		t.Fatal(err)
	}
	return u.ID, tok
}

func (e *env) login(name, pw, ip string) (accounts.Tokens, error) {
	return e.svc.Login(context.Background(), accounts.LoginInput{Username: name, Password: pw, Remember: true,
		Client: accounts.ClientInfo{IP: ip}})
}

func TestBootstrapCreatesExactlyOneAdmin(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	results := make(chan error, 6)
	for i := range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.svc.Bootstrap(ctx, "admin"+string(rune('a'+i)))
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	ok := 0
	for err := range results {
		if err == nil {
			ok++
		}
	}
	if ok != 1 {
		t.Fatalf("%d bootstraps succeeded, want exactly 1 (AUTH-U7, SEC-AUTH-13)", ok)
	}
	if _, err := e.svc.Bootstrap(ctx, "another"); !errors.Is(err, accounts.ErrConflict) {
		t.Fatalf("second bootstrap: %v", err)
	}
	var status string
	var admin bool
	if err := e.db.Admin.QueryRow(ctx, `select status, is_admin from users where is_admin`).Scan(&status, &admin); err != nil || status != "pending" {
		t.Fatalf("admin state: %v %v", status, err)
	}
}

// Activation links are single use and set the password without the admin choosing it (AUTH-U8, SEC-AUTH-4).
func TestActivation(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u, _ := e.svc.CreateUser(ctx, accounts.Actor{Kind: "system"}, accounts.CreateUserInput{Username: "Alice"})
	if u.Username != "alice" {
		t.Fatalf("usernames are normalised to lower case: %q", u.Username)
	}
	if _, err := e.svc.CreateUser(ctx, accounts.Actor{Kind: "system"}, accounts.CreateUserInput{Username: "ALICE"}); !errors.Is(err, accounts.ErrConflict) {
		t.Fatalf("duplicate username differing in case: %v", err)
	}
	link, err := e.svc.IssueActivation(ctx, accounts.Actor{Kind: "system"}, u.ID)
	if err != nil {
		t.Fatal(err)
	}

	// A weak password is refused and does not burn the link.
	if _, err := e.svc.Activate(ctx, link.Token, "password123", accounts.ClientInfo{}); !errors.Is(err, accounts.ErrInvalidInput) {
		t.Fatalf("weak password: %v", err)
	}
	if _, err := e.svc.Activate(ctx, "not-a-token", goodPassword, accounts.ClientInfo{}); !errors.Is(err, accounts.ErrInvalidToken) {
		t.Fatalf("unknown token: %v", err)
	}
	tok, err := e.svc.Activate(ctx, link.Token, goodPassword, accounts.ClientInfo{IP: "1.1.1.1"})
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if tok.Principal.Username != "alice" {
		t.Fatalf("principal: %+v", tok.Principal)
	}
	// Single use (SEC-AUTH-4).
	if _, err := e.svc.Activate(ctx, link.Token, goodPassword, accounts.ClientInfo{}); !errors.Is(err, accounts.ErrInvalidToken) {
		t.Fatalf("token reuse: %v", err)
	}
	// Only the hash of the token is stored (SEC-DATA-2).
	var n int
	if err := e.db.Admin.QueryRow(ctx, `select count(*) from user_tokens where encode(token_hash,'escape') like '%'||$1||'%'`, link.Token).Scan(&n); err != nil || n != 0 {
		t.Fatalf("activation token stored in the clear: n=%d err=%v", n, err)
	}
}

func TestActivationTokenExpires(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u, _ := e.svc.CreateUser(ctx, accounts.Actor{Kind: "system"}, accounts.CreateUserInput{Username: "bob"})
	link, _ := e.svc.IssueActivation(ctx, accounts.Actor{Kind: "system"}, u.ID)
	e.clk.Advance(8 * 24 * time.Hour)
	if _, err := e.svc.Activate(ctx, link.Token, goodPassword, accounts.ClientInfo{}); !errors.Is(err, accounts.ErrInvalidToken) {
		t.Fatalf("expired token: %v", err)
	}
}

// Password login and per-request authentication (AUTH-C1, SEC-AUTH-1).
func TestLoginAndAuthenticate(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id, _ := e.activeUser(t, "alice")

	tok, err := e.login("Alice", goodPassword, "2.2.2.2")
	if err != nil {
		t.Fatal(err)
	}
	p, err := e.svc.Authenticate(ctx, tok.Access)
	if err != nil || p.UserID != id {
		t.Fatalf("authenticate: %+v %v", p, err)
	}
	if _, err := e.svc.Authenticate(ctx, tok.Refresh); err == nil {
		t.Fatal("a refresh token must not authenticate API calls")
	}

	// An access token lapses after 15 minutes.
	e.clk.Advance(16 * time.Minute)
	if _, err := e.svc.Authenticate(ctx, tok.Access); !errors.Is(err, accounts.ErrUnauthenticated) {
		t.Fatalf("expired access token: %v", err)
	}
}

// Login is rate limited and brute-force resistant (AUTH-C6).
func TestLoginFailuresAreIndistinguishableAndThrottled(t *testing.T) {
	e := newEnv(t)
	e.activeUser(t, "alice")

	_, errWrong := e.login("alice", "wrong password!!", "3.3.3.3")
	_, errUnknown := e.login("nobody", "wrong password!!", "3.3.3.4")
	if !errors.Is(errWrong, accounts.ErrInvalidCredentials) || !errors.Is(errUnknown, accounts.ErrInvalidCredentials) {
		t.Fatalf("errors must be identical: %v / %v", errWrong, errUnknown)
	}

	// After a failure the account is throttled for that address, even for the correct password, and that
	// holds whether or not the account exists (SEC-AUTH-2, SEC-AUTH-3).
	var th *accounts.ThrottledError
	if _, err := e.login("alice", goodPassword, "3.3.3.3"); !errors.As(err, &th) {
		t.Fatalf("alice: want throttled, got %v", err)
	}
	if _, err := e.login("nobody", "x", "3.3.3.4"); !errors.As(err, &th) {
		t.Fatalf("an unknown account must throttle like a real one, got %v", err)
	}

	// Waiting lifts the block; there is no permanent lock (SEC-AUTH-2).
	e.clk.Advance(3 * time.Second)
	if _, err := e.login("alice", goodPassword, "3.3.3.3"); err != nil {
		t.Fatalf("after the delay: %v", err)
	}
	// Success resets the counter.
	if _, err := e.login("alice", goodPassword, "3.3.3.3"); err != nil {
		t.Fatalf("second login right after success: %v", err)
	}
}

// An outsider who keeps guessing a known username from their own address does not keep the real user out:
// the person signing in from anywhere else gets straight in (SEC-AUTH-2, SR-002).
func TestOutsiderCannotLockTheRealUserOut(t *testing.T) {
	e := newEnv(t)
	e.activeUser(t, "alice")
	for range 12 {
		_, err := e.login("alice", "guess guess guess", "66.66.66.66")
		var th *accounts.ThrottledError
		if errors.As(err, &th) { // the attacker patiently waits, and guesses again
			e.clk.Advance(th.RetryAfter + time.Millisecond)
			_, _ = e.login("alice", "guess guess guess", "66.66.66.66")
		}
	}
	var th *accounts.ThrottledError
	if _, err := e.login("alice", goodPassword, "66.66.66.66"); !errors.As(err, &th) {
		t.Fatalf("the attacker's own address must be slowed down, got %v", err)
	}
	if _, err := e.login("alice", goodPassword, "10.1.2.3"); err != nil {
		t.Fatalf("the real user, from another address, was locked out: %v", err)
	}
}

// Guessing that is spread over many addresses is slowed down too, but never for longer than half a
// minute, so it cannot become a lock-out either (SEC-AUTH-2).
func TestGuessingFromManyAddressesIsSlowedButNeverLocksOut(t *testing.T) {
	e := newEnv(t)
	e.activeUser(t, "alice")
	for i := range 80 {
		_, _ = e.login("alice", "guess guess guess", fmt.Sprintf("77.0.%d.%d", i/200, i%200+1))
	}
	_, err := e.login("alice", goodPassword, "10.9.9.9")
	var th *accounts.ThrottledError
	if !errors.As(err, &th) {
		t.Fatalf("after 80 distributed guesses the account must be slowed down, got %v", err)
	}
	if th.RetryAfter > 30*time.Second {
		t.Fatalf("the account-wide delay must stay short, got %v", th.RetryAfter)
	}
	e.clk.Advance(th.RetryAfter + time.Second)
	if _, err := e.login("alice", goodPassword, "10.9.9.9"); err != nil {
		t.Fatalf("after the short delay: %v", err)
	}
}

// Attempts that arrive at the same moment cannot all pass: counting and checking are one step per key,
// so a burst gets one guess, not a thousand (SEC-AUTH-2, SR-001).
func TestBurstOfLoginAttemptsIsLimited(t *testing.T) {
	e := newEnv(t)
	e.activeUser(t, "alice")
	var wg sync.WaitGroup
	var mu sync.Mutex
	wrong := 0
	for range 60 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := e.login("alice", "guess guess guess", "88.88.88.88"); errors.Is(err, accounts.ErrInvalidCredentials) {
				mu.Lock()
				wrong++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wrong != 1 {
		t.Fatalf("%d of 60 simultaneous guesses were evaluated, want exactly 1", wrong)
	}
}

// Checking the password for a sensitive action has its own throttle: wrong guesses there never lock the
// account's sign-in (SR-006).
func TestPasswordConfirmationDoesNotLockSignIn(t *testing.T) {
	e := newEnv(t)
	_, tok := e.activeUser(t, "alice")
	p := tok.Principal
	for range 6 {
		err := e.svc.ConfirmPassword(context.Background(), p, "not the password")
		var th *accounts.ThrottledError
		if errors.As(err, &th) {
			e.clk.Advance(th.RetryAfter + time.Millisecond)
		}
	}
	if _, err := e.login("alice", goodPassword, "5.5.5.5"); err != nil {
		t.Fatalf("sign-in was locked by wrong guesses at the confirmation: %v", err)
	}
	e.clk.Advance(time.Hour)
	if err := e.svc.ConfirmPassword(context.Background(), p, goodPassword); err != nil {
		t.Fatalf("right password: %v", err)
	}
}

func TestLoginDelayGrowsAndIsCapped(t *testing.T) {
	e := newEnv(t)
	e.activeUser(t, "alice")
	var last time.Duration
	for i := range 8 {
		_, err := e.login("alice", "wrong password!!", "4.4.4.4")
		var th *accounts.ThrottledError
		if errors.As(err, &th) { // still blocked from the previous failure: wait it out
			e.clk.Advance(th.RetryAfter + time.Millisecond)
			_, err = e.login("alice", "wrong password!!", "4.4.4.4")
		}
		if !errors.Is(err, accounts.ErrInvalidCredentials) {
			t.Fatalf("attempt %d: %v", i, err)
		}
		_, err = e.login("alice", goodPassword, "4.4.4.4")
		if !errors.As(err, &th) {
			t.Fatalf("attempt %d: want throttled, got %v", i, err)
		}
		if th.RetryAfter < last {
			t.Fatalf("delay must not shrink: %v after %v", th.RetryAfter, last)
		}
		last = th.RetryAfter
		e.clk.Advance(last + time.Millisecond)
	}
	if last > 15*time.Minute {
		t.Fatalf("delay above the cap: %v", last)
	}
}

func TestRefreshRotationAndGraceWindow(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, tok := e.activeUser(t, "alice")

	first, err := e.svc.Refresh(ctx, tok.Refresh)
	if err != nil {
		t.Fatal(err)
	}
	if first.Refresh == tok.Refresh {
		t.Fatal("refresh token must rotate")
	}
	// A second tab presenting the old token inside the grace window gets the same successor
	// and nobody is signed out (SEC-AUTH-7).
	e.clk.Advance(30 * time.Second)
	second, err := e.svc.Refresh(ctx, tok.Refresh)
	if err != nil {
		t.Fatalf("within grace: %v", err)
	}
	if second.Refresh != first.Refresh {
		t.Fatal("grace window must return the same successor")
	}
	if _, err := e.svc.Authenticate(ctx, second.Access); err != nil {
		t.Fatalf("session still valid after a benign race: %v", err)
	}

	// The old token after the grace window is a replay: the session is revoked (SEC-AUTH-7).
	e.clk.Advance(2 * time.Minute)
	reuses := func() float64 { return testutil.ToFloat64(obs.AuthEvents.WithLabelValues("refresh_reuse")) }
	reusesBefore := reuses()
	if _, err := e.svc.Refresh(ctx, tok.Refresh); !errors.Is(err, accounts.ErrUnauthenticated) {
		t.Fatalf("reuse: %v", err)
	}
	if got := reuses() - reusesBefore; got != 1 { // NFR-O2, SEC-AUD-3: a replayed token shows in the metrics
		t.Fatalf("refresh_reuse counted %v times, want 1", got)
	}
	if _, err := e.svc.Authenticate(ctx, first.Access); !errors.Is(err, accounts.ErrUnauthenticated) {
		t.Fatalf("session must be revoked after reuse: %v", err)
	}
	if _, err := e.svc.Refresh(ctx, first.Refresh); !errors.Is(err, accounts.ErrUnauthenticated) {
		t.Fatalf("the legitimate refresh token is dead too: %v", err)
	}
	var reason string
	if err := e.db.Admin.QueryRow(ctx, `select revoked_reason from sessions`).Scan(&reason); err != nil || reason != "refresh_reuse" {
		t.Fatalf("revoked_reason = %q %v", reason, err)
	}
	var audits int
	_ = e.db.Admin.QueryRow(ctx, `select count(*) from audit_log where action = 'session.refresh_reuse'`).Scan(&audits)
	if audits != 1 {
		t.Fatalf("reuse must be audited, got %d", audits)
	}
}

func TestConcurrentRefreshRace(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, tok := e.activeUser(t, "alice")

	const n = 10
	results := make(chan string, n)
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := e.svc.Refresh(ctx, tok.Refresh)
			errs <- err
			results <- r.Refresh
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("a refresh race must not fail: %v", err)
		}
	}
	seen := map[string]bool{}
	for r := range results {
		seen[r] = true
	}
	if len(seen) != 1 {
		t.Fatalf("%d different successors, want 1", len(seen))
	}
}

// Persistent, sliding and absolute session lifetimes, and the shared-computer option (AUTH-C9, AUTH-C11).
func TestSessionLifetimes(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.activeUser(t, "alice")

	// Sliding: use within the idle lifetime keeps the session alive indefinitely (until the absolute cap).
	tok, _ := e.login("alice", goodPassword, "5.5.5.5")
	for range 3 {
		e.clk.Advance(80 * 24 * time.Hour)
		var err error
		if tok, err = e.svc.Refresh(ctx, tok.Refresh); err != nil {
			t.Fatalf("sliding renewal: %v", err)
		}
	}
	// 240 days in: still valid. Now the absolute cap (365 days) ends it regardless of activity.
	e.clk.Advance(80 * 24 * time.Hour)
	if _, err := e.svc.Refresh(ctx, tok.Refresh); err != nil {
		// 320 days: still under the cap.
		t.Fatalf("refresh at 320 days: %v", err)
	}
	e.clk.Advance(80 * 24 * time.Hour)
	if _, err := e.svc.Refresh(ctx, tok.Refresh); !errors.Is(err, accounts.ErrUnauthenticated) {
		t.Fatalf("absolute lifetime must end the session: %v", err)
	}

	// Idle: no use for longer than the idle lifetime.
	tok2, _ := e.login("alice", goodPassword, "5.5.5.6")
	e.clk.Advance(91 * 24 * time.Hour)
	if _, err := e.svc.Refresh(ctx, tok2.Refresh); !errors.Is(err, accounts.ErrUnauthenticated) {
		t.Fatalf("idle lifetime: %v", err)
	}

	// "Do not remember me": 12 hours, session cookies (AUTH-C11).
	tok3, err := e.svc.Login(ctx, accounts.LoginInput{Username: "alice", Password: goodPassword, Remember: false, Client: accounts.ClientInfo{IP: "5.5.5.7"}})
	if err != nil {
		t.Fatal(err)
	}
	if tok3.Persistent || tok3.SessionExpiry.Sub(e.clk.Now()) > 12*time.Hour {
		t.Fatalf("session-only login: %+v", tok3)
	}
	e.clk.Advance(13 * time.Hour)
	if _, err := e.svc.Refresh(ctx, tok3.Refresh); !errors.Is(err, accounts.ErrUnauthenticated) {
		t.Fatalf("session-only lifetime: %v", err)
	}
}

// Every security event that must end access does so at once (SEC-AUTH-6, AUTH-U4).
func TestSecurityEventsRevokeSessions(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id, tok := e.activeUser(t, "alice")
	other, _ := e.login("alice", goodPassword, "6.6.6.6")

	// Password change keeps the current session and revokes the others (AUTH-U4).
	p, _ := e.svc.Authenticate(ctx, tok.Access)
	if err := e.svc.ChangePassword(ctx, *p, "wrong password!!", "another long password", accounts.ClientInfo{}); !errors.Is(err, accounts.ErrInvalidCredentials) {
		t.Fatalf("wrong current password: %v", err)
	}
	e.clk.Advance(time.Minute) // clear the throttle from the failed attempt
	if err := e.svc.ChangePassword(ctx, *p, goodPassword, "another long password", accounts.ClientInfo{}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Authenticate(ctx, tok.Access); err != nil {
		t.Fatalf("current session must survive: %v", err)
	}
	if _, err := e.svc.Authenticate(ctx, other.Access); err == nil {
		t.Fatal("other sessions must be revoked by a password change")
	}
	if _, err := e.login("alice", goodPassword, "6.6.6.7"); err == nil {
		t.Fatal("old password still works")
	}

	// An admin-issued activation link clears the password and signs everyone out (D3, SEC-ADM-2).
	if _, err := e.svc.IssueActivation(ctx, accounts.Actor{Kind: "admin"}, id); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Authenticate(ctx, tok.Access); err == nil {
		t.Fatal("activation link must revoke sessions")
	}
	e.clk.Advance(time.Hour)
	if _, err := e.login("alice", "another long password", "6.6.6.8"); err == nil {
		t.Fatal("password must be cleared by the activation link")
	}

	// Disabling revokes sessions and prevents login; enabling a user without password leaves it pending.
	id2, tok2 := e.activeUser(t, "bob")
	if err := e.svc.SetDisabled(ctx, accounts.Actor{Kind: "admin"}, id2, true); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Authenticate(ctx, tok2.Access); err == nil {
		t.Fatal("disabled user still authenticates")
	}
	if _, err := e.login("bob", goodPassword, "6.6.6.9"); err == nil {
		t.Fatal("disabled user can log in")
	}
	if err := e.svc.SetDisabled(ctx, accounts.Actor{Kind: "admin"}, id2, false); err != nil {
		t.Fatal(err)
	}
	e.clk.Advance(time.Minute) // the refused login above counted as a failure
	if _, err := e.login("bob", goodPassword, "6.6.6.9"); err != nil {
		t.Fatalf("re-enabled user: %v", err)
	}
}

// Listing and revoking sessions, and "sign out everywhere" (AUTH-U10, SEC-AUTH-16).
func TestSessionListAndRevocation(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, a := e.activeUser(t, "alice")
	_, b := e.activeUser(t, "bob")
	second, _ := e.login("alice", goodPassword, "7.7.7.7")

	pa, _ := e.svc.Authenticate(ctx, a.Access)
	list, err := e.svc.ListSessions(ctx, *pa)
	if err != nil || len(list) != 2 {
		t.Fatalf("list: %v %v", list, err)
	}
	if list[0].Label == "" {
		t.Fatal("sessions must have a label")
	}

	// Bob cannot revoke Alice's session: it looks like it does not exist (SEC-ISO-3).
	pb, _ := e.svc.Authenticate(ctx, b.Access)
	if err := e.svc.RevokeSession(ctx, *pb, pa.SessionID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("foreign revoke: %v", err)
	}
	ps, _ := e.svc.Authenticate(ctx, second.Access)
	if err := e.svc.RevokeSession(ctx, *pa, ps.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Authenticate(ctx, second.Access); err == nil {
		t.Fatal("revoked session still valid")
	}
	if err := e.svc.RevokeAllSessions(ctx, *pa, false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Authenticate(ctx, a.Access); err == nil {
		t.Fatal("sign out everywhere must include the current session when asked")
	}
	if _, err := e.svc.Authenticate(ctx, b.Access); err != nil {
		t.Fatal("sign out everywhere must not touch other users")
	}
}

func TestSessionLabel(t *testing.T) {
	cases := map[string]string{
		"Mozilla/5.0 (X11; Linux x86_64; rv:130.0) Gecko/20100101 Firefox/130.0":                                                           "Firefox on Linux",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128 Safari/537.36 Edg/128":                "Edge on Windows",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile Safari/604.1": "Safari on iOS",
		"": "Browser",
	}
	for ua, want := range cases {
		if got := accounts.SessionLabel(ua); got != want {
			t.Errorf("SessionLabel(%q) = %q, want %q", ua, got, want)
		}
	}
}

// Activation is reachable without signing in and hashing is expensive: a bad token is refused before any
// hashing, and one address cannot keep trying (SEC-API-4, SEC-AUTH-4, SR-003).
func TestActivationWithBadTokensIsCheapAndThrottled(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	client := accounts.ClientInfo{IP: "5.6.7.8"}
	var th *accounts.ThrottledError
	for i := range 11 { // ten free attempts; the block that follows the eleventh applies to the twelfth
		if _, err := e.svc.Activate(ctx, "not-a-real-token", goodPassword, client); !errors.Is(err, accounts.ErrInvalidToken) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if _, err := e.svc.Activate(ctx, "not-a-real-token", goodPassword, client); !errors.As(err, &th) {
		t.Fatalf("the twelfth attempt from one address must be throttled, got %v", err)
	}
	// Another address is unaffected, and a bad token does not reach the hasher: the answer is instant even
	// when every hashing slot is taken.
	start := time.Now()
	if _, err := e.svc.Activate(ctx, "not-a-real-token", goodPassword, accounts.ClientInfo{IP: "5.6.7.9"}); !errors.Is(err, accounts.ErrInvalidToken) {
		t.Fatalf("another address: %v", err)
	}
	if time.Since(start) > 50*time.Millisecond {
		t.Fatalf("a bad token took %v: it must be refused before hashing", time.Since(start))
	}
}
