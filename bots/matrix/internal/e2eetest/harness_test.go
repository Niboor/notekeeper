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

// startSynapse starts the development Synapse from deploy/synapse and returns its base URL.
func startSynapse(t *testing.T) string {
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
		t.Fatalf("start synapse: %v", err)
	}
	testcontainers.CleanupContainer(t, c)
	host, err := c.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := c.MappedPort(ctx, "8008/tcp")
	if err != nil {
		t.Fatal(err)
	}
	url := fmt.Sprintf("http://%s:%s", host, port.Port())
	resp, err := http.Get(url + "/_matrix/client/versions")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("synapse not answering: %v", err)
	}
	_ = resp.Body.Close()
	return url
}

// postgresDB is a PostgreSQL server from which each actor gets its own database.
type postgresDB struct{ adminURL, base string }

func startPostgres(t *testing.T) *postgresDB {
	t.Helper()
	ctx := context.Background()
	c, err := postgres.Run(ctx, "postgres:16",
		postgres.WithDatabase("postgres"), postgres.WithUsername("nk"), postgres.WithPassword("nk"),
		postgres.BasicWaitStrategies())
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	testcontainers.CleanupContainer(t, c)
	url, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	return &postgresDB{adminURL: url}
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
