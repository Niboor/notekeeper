package config

import (
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func full() map[string]string {
	return map[string]string{
		"MX_HOMESERVER": "https://matrix.example.org/", "MX_USER": "notekeeper", "MX_PASSWORD": "pw-secret-value",
		"MX_PICKLE_KEY": "test-only-pickle-key", "NK_BOT_DATABASE_URL": "postgres://x", "NK_CORE_URL": "http://core:8081/",
		"NK_BOT_KEY": "nkb.a.b",
	}
}

func TestLoadDefaultsAndTrimming(t *testing.T) {
	c, err := Load(env(full()))
	if err != nil {
		t.Fatal(err)
	}
	if c.Homeserver != "https://matrix.example.org" || c.CoreURL != "http://core:8081" {
		t.Fatalf("trailing slashes: %q %q", c.Homeserver, c.CoreURL)
	}
	if c.InstanceName != "matrix" || c.ListenAddr != ":9091" {
		t.Fatalf("defaults: %+v", c)
	}
}

func TestLoadNamesEverythingThatIsMissing(t *testing.T) {
	_, err := Load(env(map[string]string{"MX_USER": "x"}))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, name := range []string{"MX_HOMESERVER", "MX_PASSWORD", "MX_PICKLE_KEY", "NK_BOT_KEY", "NK_CORE_URL", "NK_BOT_DATABASE_URL"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error should mention %s: %v", name, err)
		}
	}
	if strings.Contains(err.Error(), "MX_USER") {
		t.Errorf("MX_USER was set: %v", err)
	}
}

func TestLoadRefusesWeakOrPlaceholderSecrets(t *testing.T) {
	m := full()
	m["MX_PICKLE_KEY"] = "short"
	if _, err := Load(env(m)); err == nil {
		t.Error("short pickle key accepted")
	}
	m = full()
	m["NK_BOT_KEY"] = "nkb.change-me.change-me"
	if _, err := Load(env(m)); err == nil {
		t.Error("placeholder secret accepted (SEC-OPS-1)")
	}
}

func TestAllowedDomainsAreOptionalAndNormalised(t *testing.T) {
	base := map[string]string{"MX_HOMESERVER": "https://m.example", "MX_USER": "bot", "MX_PASSWORD": "pw", "MX_PICKLE_KEY": strings.Repeat("k", 16), "NK_BOT_DATABASE_URL": "postgres://x", "NK_CORE_URL": "http://core", "NK_BOT_KEY": "nkb.a.b"}
	get := func(extra string) func(string) string {
		return func(k string) string {
			if k == "MX_ALLOWED_DOMAINS" {
				return extra
			}
			return base[k]
		}
	}
	if c, err := Load(get("")); err != nil || len(c.AllowedDomains) != 0 {
		t.Fatalf("default: %+v %v", c.AllowedDomains, err)
	}
	if c, err := Load(get(" One.example, two.example ,")); err != nil || len(c.AllowedDomains) != 2 || c.AllowedDomains[0] != "one.example" {
		t.Fatalf("list: %+v %v", c.AllowedDomains, err)
	}
}

func TestBotRefusesThePublishedDevelopmentPassword(t *testing.T) {
	env := map[string]string{"MX_HOMESERVER": "https://m.example", "MX_USER": "bot", "MX_PASSWORD": "pw", "MX_PICKLE_KEY": strings.Repeat("k", 16), "NK_BOT_DATABASE_URL": "postgres://nk_bot_matrix:nk_bot_dev@db/x", "NK_CORE_URL": "http://core", "NK_BOT_KEY": "nkb.a.b"}
	get := func(k string) string { return env[k] }
	if _, err := Load(get); err == nil {
		t.Fatal("the development password was accepted")
	}
	env["NK_ALLOW_DEV_CREDENTIALS"] = "true"
	if _, err := Load(get); err != nil {
		t.Fatalf("development opt-in: %v", err)
	}
	env["NK_ALLOW_DEV_CREDENTIALS"], env["NK_BOT_DATABASE_URL"] = "", "postgres://nk_bot_matrix:real-secret@db/x"
	if _, err := Load(get); err != nil {
		t.Fatalf("a real password: %v", err)
	}
}
