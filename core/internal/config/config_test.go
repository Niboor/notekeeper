package config

import (
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	c, err := LoadFrom(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if c.UserAddr != ":8080" || c.BotAddr != ":8081" || c.PublicAddr != ":8082" || c.OpsAddr != ":9090" {
		t.Fatalf("unexpected listener defaults: %+v", c)
	}
	if c.ShutdownTimeout != 25*time.Second {
		t.Fatalf("shutdown timeout = %v", c.ShutdownTimeout)
	}
	if err := c.RequireDatabase(); err == nil {
		t.Fatal("expected an error without NK_DATABASE_URL")
	}
}

func TestLoadOverrides(t *testing.T) {
	env := map[string]string{
		"NK_DATABASE_URL":     "postgres://x",
		"NK_APP_HOSTS":        "App.Example.com, other.example.com",
		"NK_SHARE_HOSTS":      "share.example.net",
		"NK_SHUTDOWN_TIMEOUT": "5s",
		"NK_USER_ADDR":        "127.0.0.1:1",
	}
	c, err := LoadFrom(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if got := len(c.AppHosts); got != 2 || c.AppHosts[0] != "app.example.com" {
		t.Fatalf("app hosts = %v", c.AppHosts)
	}
	if c.ShutdownTimeout != 5*time.Second || c.UserAddr != "127.0.0.1:1" {
		t.Fatalf("overrides not applied: %+v", c)
	}
	if err := c.RequireDatabase(); err != nil {
		t.Fatal(err)
	}
}

func TestBadDuration(t *testing.T) {
	_, err := LoadFrom(func(k string) string {
		if k == "NK_SHUTDOWN_TIMEOUT" {
			return "soon"
		}
		return ""
	})
	if err == nil {
		t.Fatal("expected error for a bad duration")
	}
}

// A deployment that still has a placeholder from the example manifests does not start (SEC-OPS-1, SEC-OPS-7).
func TestPlaceholderCredentialsAreRefused(t *testing.T) {
	get := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	c, err := LoadFrom(get(map[string]string{"NK_DATABASE_URL": "postgres://nk_app:change-me@db/notekeeper"}))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.RequireDatabase(); err == nil {
		t.Error("database: a placeholder was accepted")
	}
	// The migration needs the schema owner's URL, and only that one; the serving process needs the other.
	c, _ = LoadFrom(get(map[string]string{"NK_MIGRATE_DATABASE_URL": "postgres://nk_migrate:CHANGE-ME@db/notekeeper"}))
	if err := c.RequireMigrateDatabase(); err == nil {
		t.Error("migration: a placeholder was accepted")
	}
	if c, _ = LoadFrom(get(map[string]string{})); c.RequireMigrateDatabase() == nil {
		t.Error("migration: a missing URL was accepted")
	}
	if c, _ = LoadFrom(get(map[string]string{"NK_MIGRATE_DATABASE_URL": "postgres://nk_migrate:real@db/notekeeper"})); c.RequireMigrateDatabase() != nil {
		t.Error("migration: a real URL was refused, or it needed NK_DATABASE_URL")
	}
	c, _ = LoadFrom(get(map[string]string{"NK_DATABASE_URL": "postgres://nk_app:real@db/notekeeper"}))
	if err := c.RequireDatabase(); err != nil {
		t.Fatal(err)
	}
	c, _ = LoadFrom(get(map[string]string{"NK_TOKEN_KEYS": "k1:change-me-change-me-change-me-change-me="}))
	if err := c.RequireAuth(); err == nil {
		t.Fatal("a placeholder token key was accepted")
	}
}

// Security-relevant limits are configuration, not code (SEC-BASE-5).
func TestSecurityLimitsAreConfigurable(t *testing.T) {
	env := map[string]string{
		"NK_ACCESS_TOKEN_TTL": "5m", "NK_SESSION_IDLE_LIFETIME": "24h", "NK_SESSION_ABSOLUTE_LIFETIME": "48h", "NK_REFRESH_GRACE": "10s", "NK_ACTIVATION_TTL": "1h",
		"NK_RATE_USER_PER_MIN": "11", "NK_RATE_IP_PER_MIN": "12", "NK_RATE_BOT_PER_MIN": "13", "NK_RATE_IDENTITY_PER_MIN": "14", "NK_MAX_CONCURRENT_UPLOADS": "2",
		"NK_SHARE_MAX_LIFETIME": "24h", "NK_SHARE_MAX_ACTIVE": "5", "NK_MAX_ATTACHMENT_BYTES": "1000", "NK_DEFAULT_QUOTA_BYTES": "2000",
		"NK_ARGON2_MEMORY_KIB": "1024", "NK_ARGON2_ITERATIONS": "1",
	}
	c, err := LoadFrom(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if c.AccessTokenTTL.Minutes() != 5 || c.SessionIdleLifetime.Hours() != 24 || c.SessionAbsoluteLifetime.Hours() != 48 || c.RefreshGrace.Seconds() != 10 || c.ActivationTTL.Hours() != 1 ||
		c.RateUserPerMin != 11 || c.RateIPPerMin != 12 || c.RateBotPerMin != 13 || c.RateIdentityPerMin != 14 || c.MaxConcurrentUpload != 2 ||
		c.ShareMaxLifetime.Hours() != 24 || c.ShareMaxActive != 5 || c.MaxAttachmentBytes != 1000 || c.DefaultQuotaBytes != 2000 || c.Argon2MemoryKiB != 1024 || c.Argon2Iterations != 1 {
		t.Fatalf("a limit did not follow its variable: %+v", c)
	}
}
