package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExpandHome(t *testing.T) {
	home, _ := os.UserHomeDir()
	for in, want := range map[string]string{
		"~/Videos/Flamingo":   filepath.Join(home, "Videos", "Flamingo"),
		`~\Videos\Flamingo`:   filepath.Join(home, "Videos", "Flamingo"),
		"/srv/media/../media": "/srv/media",
	} {
		if got, err := ExpandHome(in); err != nil || got != want {
			t.Errorf("%s = %s %v, want %s", in, got, err, want)
		}
	}
	if _, err := ExpandHome("Videos"); err == nil {
		t.Error("a relative folder was accepted")
	}
}

func TestAgentSaveLoad(t *testing.T) {
	p := filepath.Join(t.TempDir(), "agent.yaml")
	c, err := LoadAgent(p)
	if err != nil || c.Listen != "127.0.0.1:8421" || c.DeviceName == "" {
		t.Fatalf("defaults = %+v %v", c, err)
	}
	c.ServerURL, c.DeviceName = "http://srv:8430/", "desk"
	if err := SaveAgent(p, c); err != nil {
		t.Fatal(err)
	}
	c2, err := LoadAgent(p)
	if err != nil || c2.ServerURL != "http://srv:8430" || c2.DeviceName != "desk" || c2.Download.MaxHeight != 1080 {
		t.Errorf("reloaded = %+v %v", c2, err)
	}
	if SafeDeviceName("Jair's MacBook Pro") != "jair-s-macbook-pro" {
		t.Error(SafeDeviceName("Jair's MacBook Pro"))
	}
}
