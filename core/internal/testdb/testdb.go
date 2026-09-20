// Package testdb gives integration tests real PostgreSQL databases cheaply. One container is
// started per test binary; the schema is migrated once into a template database and every test
// clones it (`create database ... template`), which takes tens of milliseconds and keeps real
// commit semantics. Transaction-per-test rollback is deliberately not used: NOTIFY, the change
// feed, outbox leases and SKIP LOCKED all depend on commits.
package testdb

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/Niboor/notekeeper/core/internal/db"
)

const templateName = "nk_template"

var (
	mu        sync.Mutex
	container *postgres.PostgresContainer
	adminURL  string // superuser, database "postgres"
	counter   atomic.Int64
)

// Image returns the PostgreSQL image used by ordinary integration tests (NK_TEST_PG_IMAGE, default 16).
func Image() string {
	if v := os.Getenv("NK_TEST_PG_IMAGE"); v != "" {
		return v
	}
	return "postgres:16"
}

// Main runs a package's tests and terminates the shared container afterwards. Use it from TestMain.
func Main(m interface{ Run() int }) int {
	code := m.Run()
	mu.Lock()
	defer mu.Unlock()
	if container != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = container.Terminate(ctx)
	}
	return code
}

func rolesSQL() (string, error) {
	_, file, _, _ := runtime.Caller(0)
	b, err := os.ReadFile(filepath.Join(filepath.Dir(file), "../../../deploy/sql/roles.sql"))
	return string(b), err
}

func start(ctx context.Context) error {
	mu.Lock()
	defer mu.Unlock()
	if container != nil {
		return nil
	}
	c, err := postgres.Run(ctx, Image(),
		postgres.WithDatabase("postgres"), postgres.WithUsername("nk"), postgres.WithPassword("nk"),
		// Throwaway data: durability settings only slow the tests down.
		testcontainers.WithCmdArgs("-c", "fsync=off", "-c", "full_page_writes=off",
			"-c", "synchronous_commit=off", "-c", "max_connections=400"),
		postgres.BasicWaitStrategies())
	if err != nil {
		return fmt.Errorf("start postgres: %w", err)
	}
	u, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return err
	}
	roles, err := rolesSQL()
	if err != nil {
		return err
	}
	conn, err := pgx.Connect(ctx, u)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, `create database `+templateName); err != nil {
		return err
	}
	// The roles script runs inside the application database, as documented for operators: roles
	// are cluster-wide, and the bot's schema is created in the database it is run against.
	tpl, err := pgx.Connect(ctx, withDB(u, templateName, "", ""))
	if err != nil {
		return err
	}
	defer func() { _ = tpl.Close(ctx) }()
	if _, err := tpl.Exec(ctx, roles); err != nil {
		return fmt.Errorf("roles: %w", err)
	}
	if err := db.Migrate(ctx, withDB(u, templateName, "nk_migrate", "nk_migrate_dev")); err != nil {
		return fmt.Errorf("migrate template: %w", err)
	}
	container, adminURL = c, u
	return nil
}

func withDB(raw, name, user, password string) string {
	u, _ := url.Parse(raw)
	u.Path = "/" + name
	if user != "" {
		u.User = url.UserPassword(user, password)
	}
	return u.String()
}

// DB is one isolated database for one test.
type DB struct {
	Name string
	// App connects as the runtime role nk_app: row-level security applies. This is what the
	// application under test must use.
	App    *pgxpool.Pool
	AppURL string
	// Admin connects as the superuser, bypassing row-level security. Only for fixtures and
	// for inspecting state from the outside.
	Admin    *pgxpool.Pool
	AdminURL string
}

// New clones the migrated template into a fresh database and removes it when the test ends.
func New(t testing.TB) *DB {
	t.Helper()
	ctx := context.Background()
	if err := start(ctx); err != nil {
		t.Fatalf("testdb: %v", err)
	}
	name := fmt.Sprintf("nk_t%d_%d", os.Getpid(), counter.Add(1))
	conn, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, fmt.Sprintf(`create database %s template %s`, name, templateName)); err != nil {
		t.Fatalf("clone template: %v", err)
	}
	d := &DB{Name: name}
	d.AdminURL = withDB(adminURL, name, "", "")
	d.AppURL = withDB(adminURL, name, "nk_app", "nk_app_dev")
	d.Admin, err = pgxpool.New(ctx, d.AdminURL)
	if err != nil {
		t.Fatal(err)
	}
	d.App, err = pgxpool.New(ctx, d.AppURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		d.App.Close()
		d.Admin.Close()
		c, err := pgx.Connect(context.Background(), adminURL)
		if err != nil {
			return
		}
		defer func() { _ = c.Close(context.Background()) }()
		_, _ = c.Exec(context.Background(), fmt.Sprintf(`drop database if exists %s with (force)`, name))
	})
	return d
}
