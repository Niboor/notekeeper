//go:build integration

package server_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Niboor/notekeeper/core/internal/accounts"
	"github.com/Niboor/notekeeper/core/internal/app"
	"github.com/Niboor/notekeeper/core/internal/config"
	"github.com/Niboor/notekeeper/core/internal/realtime"
	"github.com/Niboor/notekeeper/core/internal/server"
	"github.com/Niboor/notekeeper/core/internal/store"
	"github.com/Niboor/notekeeper/core/internal/testdb"
)

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

const password = "correct horse battery staple"

// stack is a complete Core (user and bot listeners on real HTTP servers) over a fresh database.
// syncBuffer collects what Core logged, so tests can prove that secrets never reach the logs.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type stack struct {
	logs   *syncBuffer
	t      *testing.T
	db     *testdb.DB
	st     *store.Store
	svc    *app.Services
	user   *httptest.Server
	bot    *httptest.Server
	public *httptest.Server
	hub    *realtime.Hub
	cfg    config.Config
}

func newStack(t *testing.T) *stack { return newStackWith(t, nil) }

// newStackWith builds a stack whose configuration can be adjusted first.
func newStackWith(t *testing.T, mod func(*config.Config)) *stack {
	t.Helper()
	d := testdb.New(t)
	cfg := config.Config{
		TokenKeys:      "k1:" + base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))),
		AccessTokenTTL: 15 * time.Minute, SessionIdleLifetime: 90 * 24 * time.Hour, SessionAbsoluteLifetime: 365 * 24 * time.Hour,
		RefreshGrace: 60 * time.Second, ActivationTTL: 7 * 24 * time.Hour,
		Argon2MemoryKiB: 8, Argon2Iterations: 1, Argon2Parallelism: 1, Argon2Concurrency: 8,
		MaxAttachmentBytes: 25 << 20, DefaultQuotaBytes: 2 << 30,
		AppURL: "https://app.example.net", ShareEnabled: true, ShareMaxLifetime: 30 * 24 * time.Hour, ShareMaxActive: 200, ShareURL: "https://share.example.net",
	}
	if mod != nil {
		mod(&cfg)
	}
	var logOut io.Writer = io.Discard
	if os.Getenv("NK_TEST_LOG") != "" {
		logOut = os.Stderr // request failures with their errors, for debugging a failing test
	}
	logs := &syncBuffer{}
	log := slog.New(slog.NewTextHandler(io.MultiWriter(logOut, logs), nil))
	st := store.New(d.App)
	svc, err := app.NewServices(cfg, st, log)
	if err != nil {
		t.Fatal(err)
	}
	hub := realtime.NewHub(d.AppURL, log)
	ctx, cancel := context.WithCancel(context.Background())
	go hub.Run(ctx)
	routers, err := server.NewRouters(server.Deps{Config: cfg, Log: log, Store: st, Accounts: svc.Accounts, Bots: svc.Bots,
		Notes: svc.Notes, Board: svc.Board, Blobs: svc.Blobs, Ingest: svc.Ingest, Outbox: svc.Outbox, Shares: svc.Shares, Reminders: svc.Reminders, Export: svc.Export, Hub: hub})
	if err != nil {
		t.Fatal(err)
	}
	s := &stack{logs: logs, t: t, db: d, st: st, svc: svc, hub: hub, cfg: cfg, user: httptest.NewServer(routers.User), bot: httptest.NewServer(routers.Bot), public: httptest.NewServer(routers.Public)}
	t.Cleanup(func() {
		hub.Shutdown()
		s.user.CloseClientConnections()
		s.user.Close()
		s.bot.Close()
		s.public.Close()
		cancel()
	})
	return s
}

// client behaves like the web app: it keeps cookies by hand (they are Secure, and the test
// server speaks plain HTTP) and sends the CSRF header.
type client struct {
	s       *stack
	cookies map[string]string
}

func (s *stack) newClient() *client { return &client{s: s, cookies: map[string]string{}} }

type response struct {
	Status int
	Header http.Header
	Body   []byte
}

func (r response) JSON(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal(r.Body, v); err != nil {
		t.Fatalf("decode %q: %v", r.Body, err)
	}
}

func (r response) Code() string {
	var p struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(r.Body, &p)
	return p.Code
}

func (c *client) do(method, path string, body any) response {
	c.s.t.Helper()
	return c.doWith(method, path, body, nil)
}

func (c *client) doWith(method, path string, body any, mod func(*http.Request)) response {
	c.s.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.s.user.URL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Notekeeper-Client", "web")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64; rv:130.0) Gecko/20100101 Firefox/130.0")
	for k, v := range c.cookies {
		req.AddCookie(&http.Cookie{Name: k, Value: v})
	}
	if mod != nil {
		mod(req)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		c.s.t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	for _, ck := range res.Cookies() {
		if ck.MaxAge < 0 {
			delete(c.cookies, ck.Name)
		} else {
			c.cookies[ck.Name] = ck.Value
		}
	}
	return response{Status: res.StatusCode, Header: res.Header, Body: b}
}

// botDo calls the bot API with a bot key.
func (s *stack) botDo(bearer, method, path string, body any) response {
	s.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, s.bot.URL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	return response{Status: res.StatusCode, Header: res.Header, Body: b}
}

// makeUser creates and activates a user directly through the service and returns its id.
func (s *stack) makeUser(name string, admin bool) uuid.UUID {
	s.t.Helper()
	ctx := context.Background()
	var id uuid.UUID
	if admin {
		link, err := s.svc.Accounts.Bootstrap(ctx, name)
		if err != nil {
			s.t.Fatal(err)
		}
		id = link.UserID
		if _, err := s.svc.Accounts.Activate(ctx, link.Token, password, accounts.ClientInfo{IP: "127.0.0.1"}); err != nil {
			s.t.Fatal(err)
		}
		return id
	}
	u, err := s.svc.Accounts.CreateUser(ctx, accounts.Actor{Kind: "system"}, accounts.CreateUserInput{Username: name})
	if err != nil {
		s.t.Fatal(err)
	}
	link, err := s.svc.Accounts.IssueActivation(ctx, accounts.Actor{Kind: "system"}, u.ID)
	if err != nil {
		s.t.Fatal(err)
	}
	if _, err := s.svc.Accounts.Activate(ctx, link.Token, password, accounts.ClientInfo{IP: "127.0.0.1"}); err != nil {
		s.t.Fatal(err)
	}
	return u.ID
}

// login signs the client in through the HTTP API.
func (c *client) login(name string) {
	c.s.t.Helper()
	res := c.do("POST", "/api/v1/auth/login", map[string]any{"username": name, "password": password})
	if res.Status != 200 {
		c.s.t.Fatalf("login %s: %d %s", name, res.Status, res.Body)
	}
}

// makeBot registers a Matrix bot instance and returns its bearer key.
func (s *stack) makeBot(name, domain string) string {
	s.t.Helper()
	ctx := context.Background()
	inst, err := s.svc.Bots.CreateInstance(ctx, store.Actor{Kind: "system"}, botsInput(name, domain))
	if err != nil {
		s.t.Fatal(err)
	}
	cred, err := s.svc.Bots.CreateCredential(ctx, store.Actor{Kind: "system"}, inst.ID, nil)
	if err != nil {
		s.t.Fatal(err)
	}
	return cred.Bearer
}
