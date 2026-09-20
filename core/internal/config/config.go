// Package config loads Core's configuration from environment variables (docs/design/08 section 3).
// Defaults are the safe ones (SEC-BASE-5); secrets only ever come from the environment (SEC-OPS-1).
package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the runtime configuration of the Core binary.
type Config struct {
	DatabaseURL string
	// MigrateDatabaseURL is the schema owner's connection, used only by `core migrate`.
	MigrateDatabaseURL string

	// Listener addresses: user API, bot API, public share API, ops (health and metrics).
	UserAddr   string
	BotAddr    string
	PublicAddr string
	OpsAddr    string

	// Hostnames each listener accepts (Host header check, docs/design/README section 2).
	// Empty means "accept any", which is only intended for local development.
	AppHosts   []string
	ShareHosts []string

	// Public base URLs (scheme and host): used to build links and derive the allowed hosts.
	AppURL   string
	ShareURL string

	LogLevel        string
	ShutdownTimeout time.Duration

	// Sessions and tokens (docs/design/03-auth.md).
	TokenKeys               string // "kid:base64key,..."; the first signs, all verify
	AccessTokenTTL          time.Duration
	SessionIdleLifetime     time.Duration
	SessionAbsoluteLifetime time.Duration // 0 = unlimited
	RefreshGrace            time.Duration
	ActivationTTL           time.Duration

	// Password hashing cost (SEC-BASE-1) and the number of hashes computed at once (SEC-API-4).
	Argon2MemoryKiB   uint32
	Argon2Iterations  uint32
	Argon2Parallelism uint8
	Argon2Concurrency int

	// Attachments (CORE-A3): the largest single file and the default per-user quota, in bytes.
	MaxAttachmentBytes int64
	DefaultQuotaBytes  int64

	// Sharing (CORE-SH2, CORE-SH11, SEC-SHR-11): the operator can switch it off, cap how long a link
	// may live, and cap how many links one user may hold at once.
	ShareEnabled     bool
	ShareMaxLifetime time.Duration
	ShareMaxActive   int

	// Rate limits per minute (SEC-API-4, SEC-BOT-10, NFR-S4): per address and per signed-in user on the
	// user API, per bot instance and per linked person on the bot API, and the uploads one user may run at once.
	RateIPPerMin        int
	RateUserPerMin      int
	RateBotPerMin       int
	RateIdentityPerMin  int
	MaxConcurrentUpload int

	// Ingress addresses whose X-Forwarded-For is trusted for per-address throttling.
	TrustedProxies []string
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

	c.AppURL = strings.TrimRight(getenv("NK_APP_URL"), "/")
	c.ShareURL = strings.TrimRight(getenv("NK_SHARE_URL"), "/")
	if len(c.AppHosts) == 0 {
		c.AppHosts = hostOf(c.AppURL)
	}
	if len(c.ShareHosts) == 0 {
		c.ShareHosts = hostOf(c.ShareURL)
	}
	c.MigrateDatabaseURL = getenv("NK_MIGRATE_DATABASE_URL")
	c.TokenKeys = getenv("NK_TOKEN_KEYS")
	c.TrustedProxies = list(getenv("NK_TRUSTED_PROXIES"))
	for _, f := range []struct {
		dst *time.Duration
		key string
		def time.Duration
	}{
		{&c.AccessTokenTTL, "NK_ACCESS_TOKEN_TTL", 15 * time.Minute},
		{&c.SessionIdleLifetime, "NK_SESSION_IDLE_LIFETIME", 90 * 24 * time.Hour},
		{&c.SessionAbsoluteLifetime, "NK_SESSION_ABSOLUTE_LIFETIME", 365 * 24 * time.Hour},
		{&c.RefreshGrace, "NK_REFRESH_GRACE", 60 * time.Second},
		{&c.ActivationTTL, "NK_ACTIVATION_TTL", 7 * 24 * time.Hour},
	} {
		if *f.dst, err = duration(getenv, f.key, f.def); err != nil {
			return Config{}, err
		}
	}
	mem, err := number(getenv, "NK_ARGON2_MEMORY_KIB", 64*1024)
	if err != nil {
		return Config{}, err
	}
	iter, err := number(getenv, "NK_ARGON2_ITERATIONS", 3)
	if err != nil {
		return Config{}, err
	}
	par, err := number(getenv, "NK_ARGON2_PARALLELISM", 2)
	if err != nil {
		return Config{}, err
	}
	conc, err := number(getenv, "NK_ARGON2_CONCURRENCY", 4)
	if err != nil {
		return Config{}, err
	}
	maxAtt, err := number(getenv, "NK_MAX_ATTACHMENT_BYTES", 25<<20)
	if err != nil {
		return Config{}, err
	}
	quota, err := number(getenv, "NK_DEFAULT_QUOTA_BYTES", 2<<30)
	if err != nil {
		return Config{}, err
	}
	c.MaxAttachmentBytes, c.DefaultQuotaBytes = int64(maxAtt), int64(quota)
	for _, f := range []struct {
		dst *int
		key string
		def uint64
	}{
		{&c.RateIPPerMin, "NK_RATE_IP_PER_MIN", 3000}, {&c.RateUserPerMin, "NK_RATE_USER_PER_MIN", 1800},
		{&c.RateBotPerMin, "NK_RATE_BOT_PER_MIN", 6000}, {&c.RateIdentityPerMin, "NK_RATE_IDENTITY_PER_MIN", 600},
		{&c.MaxConcurrentUpload, "NK_MAX_CONCURRENT_UPLOADS", 4},
	} {
		v, err := number(getenv, f.key, f.def)
		if err != nil {
			return Config{}, err
		}
		*f.dst = int(v)
	}
	c.ShareEnabled = strings.ToLower(env(getenv, "NK_SHARE_ENABLED", "true")) != "false"
	if c.ShareMaxLifetime, err = duration(getenv, "NK_SHARE_MAX_LIFETIME", 30*24*time.Hour); err != nil {
		return Config{}, err
	}
	shareMax, err := number(getenv, "NK_SHARE_MAX_ACTIVE", 200)
	if err != nil {
		return Config{}, err
	}
	c.ShareMaxActive = int(shareMax)
	c.Argon2MemoryKiB, c.Argon2Iterations, c.Argon2Parallelism, c.Argon2Concurrency = uint32(mem), uint32(iter), uint8(par), int(conc)
	return c, nil
}

// RequireAuth fails when the token keys are missing or still a documented placeholder
// (SEC-OPS-1, SEC-OPS-7): Core must not start with a secret anyone could know.
func (c Config) RequireAuth() error {
	if c.TokenKeys == "" {
		return fmt.Errorf("NK_TOKEN_KEYS is required (kid:base64key with at least 32 random bytes)")
	}
	if strings.Contains(strings.ToLower(c.TokenKeys), "change-me") || strings.Contains(strings.ToLower(c.TokenKeys), "changeme") {
		return fmt.Errorf("NK_TOKEN_KEYS still contains a placeholder value")
	}
	return nil
}

func number(getenv func(string) string, key string, def uint64) (uint64, error) {
	v := getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.ParseUint(v, 10, 63)
	if err != nil || n == 0 {
		return 0, fmt.Errorf("%s: must be a positive number", key)
	}
	return n, nil
}

// RequireDatabase fails when no database URL is configured.
func (c Config) RequireDatabase() error {
	if c.DatabaseURL == "" {
		return fmt.Errorf("NK_DATABASE_URL is required")
	}
	// The example manifests ship placeholders instead of credentials (SEC-OPS-1, SEC-OPS-7); a deployment
	// that forgot to replace one must not start.
	for name, v := range map[string]string{"NK_DATABASE_URL": c.DatabaseURL, "NK_MIGRATE_DATABASE_URL": c.MigrateDatabaseURL} {
		if strings.Contains(strings.ToLower(v), "change-me") {
			return fmt.Errorf("%s still contains a placeholder value", name)
		}
	}
	return nil
}

func env(getenv func(string) string, key, def string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return def
}

// hostOf returns the lower-case hostname of a URL as a one-element list, or nil.
func hostOf(raw string) []string {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return nil
	}
	return []string{strings.ToLower(u.Hostname())}
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
