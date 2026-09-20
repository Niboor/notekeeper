//go:build integration

package e2eetest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"maunium.net/go/mautrix/event"
)

// The whole chain, with nothing faked: Synapse, PostgreSQL, the real Core binary and the real
// Matrix bot. A person on "Element" (an encrypted client) talks to the bot; the web side is
// driven through Core's HTTP API exactly as the web app does.

type coreProcess struct {
	t                    *testing.T
	bin                  string
	env                  []string
	userURL, botURL, ops string
	cmd                  *exec.Cmd
	out                  *lockedBuffer
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuffer) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "..")
}

// buildCore compiles the Core binary once per test run.
func buildCore(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "core")
	cmd := exec.Command("go", "build", "-o", bin, "github.com/Niboor/notekeeper/core/cmd/core")
	cmd.Dir = repoRoot()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build core: %v\n%s", err, out)
	}
	return bin
}

func startCore(t *testing.T, pgAdminURL string) *coreProcess {
	t.Helper()
	ctx := context.Background()
	roles, err := os.ReadFile(filepath.Join(repoRoot(), "deploy", "sql", "roles.sql"))
	if err != nil {
		t.Fatal(err)
	}
	adminConn, err := pgx.Connect(ctx, pgAdminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = adminConn.Close(ctx) }()
	if _, err := adminConn.Exec(ctx, "create database notekeeper"); err != nil {
		t.Fatal(err)
	}
	cfg, _ := pgx.ParseConfig(pgAdminURL)
	appDB, err := pgx.Connect(ctx, fmt.Sprintf("postgres://%s:%s@%s:%d/notekeeper?sslmode=disable", cfg.User, cfg.Password, cfg.Host, cfg.Port))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = appDB.Close(ctx) }()
	if _, err := appDB.Exec(ctx, string(roles)); err != nil { // run inside the application database, as documented
		t.Fatal(err)
	}
	dsn := func(user, pass string) string {
		return fmt.Sprintf("postgres://%s:%s@%s:%d/notekeeper?sslmode=disable", user, pass, cfg.Host, cfg.Port)
	}

	p := &coreProcess{t: t, bin: buildCore(t), out: &lockedBuffer{}}
	userPort, botPort, publicPort, opsPort := freePort(t), freePort(t), freePort(t), freePort(t)
	p.userURL = fmt.Sprintf("http://127.0.0.1:%d", userPort)
	p.botURL = fmt.Sprintf("http://127.0.0.1:%d", botPort)
	p.ops = fmt.Sprintf("http://127.0.0.1:%d", opsPort)
	p.env = append(os.Environ(),
		"NK_ALLOW_DEV_CREDENTIALS=true", // the throwaway database of this test
		"NK_DATABASE_URL="+dsn("nk_app", "nk_app_dev"),
		"NK_TOKEN_KEYS=e2e:"+"ZTJlLW9ubHkta2V5LWUyZS1vbmx5LWtleS1lMmUtb25seS1rZXk=",
		"NK_APP_URL="+p.userURL,
		fmt.Sprintf("NK_USER_ADDR=127.0.0.1:%d", userPort), fmt.Sprintf("NK_BOT_ADDR=127.0.0.1:%d", botPort),
		fmt.Sprintf("NK_PUBLIC_ADDR=127.0.0.1:%d", publicPort), fmt.Sprintf("NK_OPS_ADDR=127.0.0.1:%d", opsPort),
		"NK_ARGON2_MEMORY_KIB=8192", "NK_LOG_LEVEL=info",
	)
	// The schema owner's credential is given to the migration alone, as in a deployment (SEC-OPS-5).
	migrate := exec.Command(p.bin, "migrate")
	migrate.Env = append(append([]string{}, p.env...), "NK_MIGRATE_DATABASE_URL="+dsn("nk_migrate", "nk_migrate_dev"))
	if out, err := migrate.CombinedOutput(); err != nil {
		t.Fatalf("migrate: %v\n%s", err, out)
	}
	p.cmd = exec.Command(p.bin, "serve")
	p.cmd.Env = p.env
	p.cmd.Stdout, p.cmd.Stderr = p.out, p.out
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = p.cmd.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { _ = p.cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			_ = p.cmd.Process.Kill()
		}
		if t.Failed() {
			t.Logf("core log:\n%s", p.out.String())
		}
	})
	deadline := time.Now().Add(30 * time.Second)
	for {
		res, err := http.Get(p.ops + "/readyz")
		if err == nil {
			_ = res.Body.Close()
			if res.StatusCode == 200 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("core did not become ready:\n%s", p.out.String())
		}
		time.Sleep(200 * time.Millisecond)
	}
	return p
}

// run executes a core subcommand (the operator CLI shares the process environment).
func (p *coreProcess) run(args ...string) (string, error) {
	cmd := exec.Command(p.bin, args...)
	cmd.Env = p.env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// webClient behaves like the web app: cookie session (kept by hand, they are Secure) plus the CSRF header.
type webClient struct {
	t       *testing.T
	base    string
	cookies map[string]string
}

func (p *coreProcess) web(t *testing.T) *webClient {
	return &webClient{t: t, base: p.userURL, cookies: map[string]string{}}
}

func (c *webClient) do(method, path string, body any) (int, []byte) {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.base+path, rd)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Notekeeper-Client", "web")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	for k, v := range c.cookies {
		req.AddCookie(&http.Cookie{Name: k, Value: v})
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	for _, ck := range res.Cookies() {
		if ck.MaxAge >= 0 {
			c.cookies[ck.Name] = ck.Value
		}
	}
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, b
}

func (c *webClient) mustJSON(method, path string, body, into any) {
	c.t.Helper()
	status, b := c.do(method, path, body)
	if status >= 300 {
		c.t.Fatalf("%s %s: %d %s", method, path, status, b)
	}
	if into != nil {
		if err := json.Unmarshal(b, into); err != nil {
			c.t.Fatalf("decode %s: %v (%s)", b, err, path)
		}
	}
}

var activationRE = regexp.MustCompile(`/activate#(\S+)`)

// TestEndToEndChatToInbox is exit criterion M1 (F1, F2): a message written in an encrypted Matrix
// chat appears in the web Inbox, the bot confirms with a reaction, and a message from someone who
// has not linked their chat is answered with instructions and stored nowhere.
func TestEndToEndChatToInbox(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	hs, pg := startSynapse(t), startPostgres(t)
	core := startCore(t, pg.adminURL)

	// Operator: bootstrap the admin, register the Matrix bot, mint its key.
	out, err := core.run("admin", "bootstrap", "robin")
	if err != nil {
		t.Fatalf("bootstrap: %v\n%s", err, out)
	}
	token := activationRE.FindStringSubmatch(out)[1]
	if out, err := core.run("admin", "bot-create", "matrix", "matrix-e2e", "localhost"); err != nil {
		t.Fatalf("bot-create: %v\n%s", err, out)
	}
	out, err = core.run("admin", "bot-credential", "matrix-e2e")
	if err != nil {
		t.Fatalf("bot-credential: %v\n%s", err, out)
	}
	botKey := regexp.MustCompile(`nkb\.\S+`).FindString(out)

	// Robin opens the activation link and signs in.
	web := core.web(t)
	web.mustJSON("POST", "/api/v1/auth/activate", map[string]any{"token": token, "password": "correct horse battery staple"}, nil)

	// The bot comes online.
	registerUser(t, hs, "notekeeper_e2e")
	cfg := botConfig(hs, pg.newDatabase(t, "bot_e2e"), core.botURL, "notekeeper_e2e")
	cfg.BotKey, cfg.InstanceName = botKey, "matrix-e2e"
	rb := startBot(t, cfg)

	// Robin (on Element) starts an encrypted direct message with the bot.
	robin := person(t, hs, pg, "robin")
	room := createDM(t, robin, rb.b.UserID(), true)
	waitMembership(t, robin.client, room, rb.b.UserID(), event.MembershipJoin, 30*time.Second)
	time.Sleep(2 * time.Second)

	// Before linking, the bot explains how to link and saves nothing.
	if _, err := robin.client.SendText(ctx, room, "hello before linking"); err != nil {
		t.Fatal(err)
	}
	waitNotice(t, robin, "create a code")

	// In the app: create a pairing code for this bot; in the chat: send it.
	var bots struct{ Items []struct{ ID, Name string } }
	web.mustJSON("GET", "/api/v1/bot-instances", nil, &bots)
	if len(bots.Items) != 1 {
		t.Fatalf("bot instances: %+v", bots)
	}
	var code struct{ Code string }
	web.mustJSON("POST", "/api/v1/me/pairing-codes", map[string]any{"bot_instance_id": bots.Items[0].ID}, &code)
	if _, err := robin.client.SendText(ctx, room, "!link "+code.Code); err != nil {
		t.Fatal(err)
	}
	waitNotice(t, robin, "Linked to your account")

	// Now a message becomes a note.
	sent, err := robin.client.SendText(ctx, room, "buy milk, eggs")
	if err != nil {
		t.Fatal(err)
	}
	waitReaction(t, robin, room, sent.EventID, "✅")

	var inbox struct {
		Items []struct {
			ID    string `json:"id"`
			Parts []struct {
				Text          string `json:"text"`
				SourceBotType string `json:"source_bot_type"`
			} `json:"parts"`
		} `json:"items"`
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		web.mustJSON("GET", "/api/v1/inbox/notes", nil, &inbox)
		if len(inbox.Items) > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if len(inbox.Items) != 1 || inbox.Items[0].Parts[0].Text != "buy milk, eggs" || inbox.Items[0].Parts[0].SourceBotType != "matrix" {
		t.Fatalf("inbox after the message: %+v (the pre-link message must not be there)", inbox)
	}

	// Unlinking from the app stops the flow: the next message is refused and not stored.
	var ids struct{ Items []struct{ ID string } }
	web.mustJSON("GET", "/api/v1/me/identities", nil, &ids)
	if status, b := web.do("DELETE", "/api/v1/me/identities/"+ids.Items[0].ID, nil); status != 204 {
		t.Fatalf("unlink: %d %s", status, b)
	}
	if _, err := robin.client.SendText(ctx, room, "after unlinking"); err != nil {
		t.Fatal(err)
	}
	// (The linking instructions were already sent within the last hour, so nothing is repeated.)
	time.Sleep(3 * time.Second)
	web.mustJSON("GET", "/api/v1/inbox/notes", nil, &inbox)
	if len(inbox.Items) != 1 {
		t.Fatalf("a message after unlinking was stored: %+v", inbox)
	}
	if strings.Contains(core.out.String(), "buy milk") {
		t.Fatal("note text leaked into Core's logs (SEC-DATA-1)")
	}
}
