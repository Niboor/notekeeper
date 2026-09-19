//go:build integration

// Package e2eetest holds integration tests that run the bot's Matrix stack against a real
// Synapse homeserver and PostgreSQL. This first test is the M0 spike: an encrypted direct
// message round trip with the Olm/Megolm state kept in PostgreSQL (MX-3, MX-N1, BOT-B5).
package e2eetest

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// repoDeploy returns the path of deploy/ in the repository.
func repoDeploy(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "deploy")
}

// One Synapse and one PostgreSQL serve every test of the package (starting them per test would
// dominate the run time); tests stay independent by using unique user and database names.
var (
	shared struct {
		once   sync.Once
		synURL string
		synErr error
		synC   testcontainers.Container
		pgOnce sync.Once
		pg     *postgresDB
		pgErr  error
		pgC    testcontainers.Container
	}
)

// TestMain terminates the shared containers after the last test.
func TestMain(m *testing.M) {
	code := m.Run()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if shared.synC != nil {
		_ = shared.synC.Terminate(ctx)
	}
	if shared.pgC != nil {
		_ = shared.pgC.Terminate(ctx)
	}
	os.Exit(code)
}

// startSynapse returns the base URL of the shared development Synapse from deploy/synapse.
func startSynapse(t *testing.T) string {
	t.Helper()
	shared.once.Do(func() { shared.synURL, shared.synC, shared.synErr = launchSynapse(t) })
	if shared.synErr != nil {
		t.Fatalf("synapse: %v", shared.synErr)
	}
	return shared.synURL
}

func launchSynapse(t *testing.T) (string, testcontainers.Container, error) {
	t.Helper()
	ctx := context.Background()
	dir := filepath.Join(repoDeploy(t), "synapse")
	image := os.Getenv("NK_TEST_SYNAPSE_IMAGE")
	if image == "" {
		image = "matrixdotorg/synapse:latest"
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		Started: true,
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        image,
			ExposedPorts: []string{"8008/tcp"},
			Entrypoint:   []string{"/bin/sh", "-c"},
			Cmd: []string{"python -m synapse.app.homeserver --config-path /data/homeserver.yaml --generate-keys && " +
				"exec python -m synapse.app.homeserver --config-path /data/homeserver.yaml"},
			Files: []testcontainers.ContainerFile{
				{HostFilePath: filepath.Join(dir, "homeserver.yaml"), ContainerFilePath: "/data/homeserver.yaml", FileMode: 0o644},
				{HostFilePath: filepath.Join(dir, "log.config"), ContainerFilePath: "/data/log.config", FileMode: 0o644},
			},
			WaitingFor: wait.ForHTTP("/health").WithPort("8008/tcp").WithStartupTimeout(3 * time.Minute),
		},
	})
	if err != nil {
		return "", nil, fmt.Errorf("start synapse: %w", err)
	}
	host, err := c.Host(ctx)
	if err != nil {
		return "", c, err
	}
	port, err := c.MappedPort(ctx, "8008/tcp")
	if err != nil {
		return "", c, err
	}
	url := fmt.Sprintf("http://%s:%s", host, port.Port())
	resp, err := http.Get(url + "/_matrix/client/versions")
	if err != nil || resp.StatusCode != 200 {
		return "", c, fmt.Errorf("synapse not answering: %v", err)
	}
	_ = resp.Body.Close()
	return url, c, nil
}

// postgresDB is a PostgreSQL server from which each actor gets its own database.
type postgresDB struct{ adminURL, base string }

func startPostgres(t *testing.T) *postgresDB {
	t.Helper()
	shared.pgOnce.Do(func() {
		ctx := context.Background()
		c, err := postgres.Run(ctx, "postgres:16",
			postgres.WithDatabase("postgres"), postgres.WithUsername("nk"), postgres.WithPassword("nk"),
			// Throwaway data: durability settings only slow the tests down.
			testcontainers.WithCmdArgs("-c", "fsync=off", "-c", "full_page_writes=off", "-c", "synchronous_commit=off", "-c", "max_connections=300"),
			postgres.BasicWaitStrategies())
		if err != nil {
			shared.pgErr = err
			return
		}
		shared.pgC = c
		url, err := c.ConnectionString(ctx, "sslmode=disable")
		if err != nil {
			shared.pgErr = err
			return
		}
		shared.pg = &postgresDB{adminURL: url}
	})
	if shared.pgErr != nil {
		t.Fatalf("postgres: %v", shared.pgErr)
	}
	return shared.pg
}

// newDatabase creates an empty database and returns its connection URL.
func (p *postgresDB) newDatabase(t *testing.T, name string) string {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, p.adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, "create database "+name); err != nil {
		t.Fatal(err)
	}
	cfg, _ := pgx.ParseConfig(p.adminURL)
	cfg.Database = name
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable", cfg.User, cfg.Password, cfg.Host, cfg.Port, name)
}
