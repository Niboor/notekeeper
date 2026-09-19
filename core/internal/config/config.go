// Package config loads Core's configuration from environment variables (docs/design/08 section 3).
// Defaults are the safe ones (SEC-BASE-5); secrets only ever come from the environment (SEC-OPS-1).
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the runtime configuration of the Core binary.
type Config struct {
	DatabaseURL string

	// Listener addresses: user API, bot API, public share API, ops (health and metrics).
	UserAddr   string
	BotAddr    string
	PublicAddr string
	OpsAddr    string

	// Hostnames each listener accepts (Host header check, docs/design/README section 2).
	// Empty means "accept any", which is only intended for local development.
	AppHosts   []string
	ShareHosts []string

	LogLevel        string
	ShutdownTimeout time.Duration
}

// Load reads the configuration from the process environment.
func Load() (Config, error) { return LoadFrom(os.Getenv) }

// LoadFrom reads the configuration through getenv (injectable for tests).
func LoadFrom(getenv func(string) string) (Config, error) {
	c := Config{
		DatabaseURL: getenv("NK_DATABASE_URL"),
		UserAddr:    env(getenv, "NK_USER_ADDR", ":8080"),
		BotAddr:     env(getenv, "NK_BOT_ADDR", ":8081"),
		PublicAddr:  env(getenv, "NK_PUBLIC_ADDR", ":8082"),
		OpsAddr:     env(getenv, "NK_OPS_ADDR", ":9090"),
		AppHosts:    list(getenv("NK_APP_HOSTS")),
		ShareHosts:  list(getenv("NK_SHARE_HOSTS")),
		LogLevel:    env(getenv, "NK_LOG_LEVEL", "info"),
	}
	d, err := duration(getenv, "NK_SHUTDOWN_TIMEOUT", 25*time.Second)
	if err != nil {
		return Config{}, err
	}
	c.ShutdownTimeout = d
	return c, nil
}

// RequireDatabase fails when no database URL is configured.
func (c Config) RequireDatabase() error {
	if c.DatabaseURL == "" {
		return fmt.Errorf("NK_DATABASE_URL is required")
	}
	return nil
}

func env(getenv func(string) string, key, def string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return def
}

func list(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, strings.ToLower(p))
		}
	}
	return out
}

func duration(getenv func(string) string, key string, def time.Duration) (time.Duration, error) {
	v := getenv(key)
	if v == "" {
		return def, nil
	}
	if secs, err := strconv.Atoi(v); err == nil {
		return time.Duration(secs) * time.Second, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return d, nil
}
