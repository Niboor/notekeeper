// Package config loads the Matrix bot's configuration from the environment (docs/design/08 section 3).
// Secrets only ever come from the environment (Kubernetes Secrets), never from files in the image.
package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// Config is the runtime configuration.
type Config struct {
	Homeserver string // https://matrix.example.org
	User       string // localpart or full MXID of the bot account
	Password   string // used to log in; the device is then kept in the crypto store
	PickleKey  []byte // encrypts device keys at rest (MX-N1)

	DatabaseURL  string // the bot's own database role and schema (SEC-OPS-5)
	InstanceName string // name of the bot instance registered in Core; also names the advisory lock

	CoreURL string // bot API base URL, cluster-internal
	BotKey  string // nkb.<client id>.<secret>

	ListenAddr string // health and metrics
	LogLevel   string

	HeartbeatEvery time.Duration
}

// Load reads the configuration through getenv.
func Load(getenv func(string) string) (Config, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	c := Config{
		Homeserver: strings.TrimRight(getenv("MX_HOMESERVER"), "/"), User: getenv("MX_USER"), Password: getenv("MX_PASSWORD"),
		PickleKey: []byte(getenv("MX_PICKLE_KEY")), DatabaseURL: getenv("NK_BOT_DATABASE_URL"),
		InstanceName: def(getenv("NK_BOT_INSTANCE"), "matrix"), CoreURL: strings.TrimRight(getenv("NK_CORE_URL"), "/"),
		BotKey: getenv("NK_BOT_KEY"), ListenAddr: def(getenv("NK_BOT_LISTEN"), ":9091"), LogLevel: def(getenv("NK_LOG_LEVEL"), "info"),
		HeartbeatEvery: 30 * time.Second,
	}
	var missing []string
	for name, v := range map[string]string{
		"MX_HOMESERVER": c.Homeserver, "MX_USER": c.User, "MX_PASSWORD": c.Password, "MX_PICKLE_KEY": string(c.PickleKey),
		"NK_BOT_DATABASE_URL": c.DatabaseURL, "NK_CORE_URL": c.CoreURL, "NK_BOT_KEY": c.BotKey,
	} {
		if v == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("missing required settings: %s", strings.Join(sortedStrings(missing), ", "))
	}
	if len(c.PickleKey) < 16 {
		return Config{}, errors.New("MX_PICKLE_KEY must be at least 16 characters")
	}
	for _, v := range []string{c.Password, string(c.PickleKey), c.BotKey} {
		if strings.Contains(strings.ToLower(v), "change-me") {
			return Config{}, errors.New("a secret still contains a placeholder value (SEC-OPS-1)")
		}
	}
	return c, nil
}

func def(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

func sortedStrings(s []string) []string {
	out := append([]string(nil), s...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}
