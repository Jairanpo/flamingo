package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "server.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestYAMLAndExpansion(t *testing.T) {
	t.Setenv("FLAMINGO_DB_PASSWORD", "s3cret")
	p := write(t, `
listen: ":9000"
database:
  url: "postgres://flamingo:${FLAMINGO_DB_PASSWORD}@db/flamingo"
auth:
  session_ttl: 2h
`)
	c, err := LoadServer(p, true)
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":9000" {
		t.Errorf("listen = %q", c.Listen)
	}
	if c.Database.URL != "postgres://flamingo:s3cret@db/flamingo" {
		t.Errorf("url = %q", c.Database.URL)
	}
	if c.Auth.SessionTTL != 2*time.Hour {
		t.Errorf("ttl = %v", c.Auth.SessionTTL)
	}
	if !c.Database.MigrateOnStart || c.Auth.Signup != "invite" || c.Ytdlp.Path != "yt-dlp" {
		t.Errorf("defaults not applied: %+v", c)
	}
}

func TestEnvOverridesFile(t *testing.T) {
	t.Setenv("FLAMINGO_DATABASE_URL", "postgres://env/flamingo")
	t.Setenv("FLAMINGO_LISTEN", ":7000")
	p := write(t, "database:\n  url: postgres://file/flamingo\n")
	c, err := LoadServer(p, true)
	if err != nil {
		t.Fatal(err)
	}
	if c.Database.URL != "postgres://env/flamingo" || c.Listen != ":7000" {
		t.Errorf("env did not win: %q %q", c.Database.URL, c.Listen)
	}
}

func TestMissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "none.yaml")
	if _, err := LoadServer(missing, true); err == nil {
		t.Error("an explicit missing file should be an error")
	}
	t.Setenv("FLAMINGO_DATABASE_URL", "postgres://env/flamingo")
	if _, err := LoadServer(missing, false); err != nil {
		t.Errorf("the default path may be absent: %v", err)
	}
}

func TestValidation(t *testing.T) {
	for name, body := range map[string]string{
		"no url":     "listen: :1\n",
		"bad signup": "database: {url: x}\nauth: {signup: open}\n",
		"zero ttl":   "database: {url: x}\nauth: {session_ttl: 0s}\n",
	} {
		if _, err := LoadServer(write(t, body), true); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}
