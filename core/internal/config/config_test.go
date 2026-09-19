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
