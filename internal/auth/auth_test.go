package auth

import (
	"strings"
	"testing"
	"time"
)

func TestPasswordRoundTrip(t *testing.T) {
	h, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Errorf("hash format: %s", h)
	}
	if ok, err := CheckPassword(h, "correct horse"); !ok || err != nil {
		t.Errorf("right password rejected: %v", err)
	}
	if ok, _ := CheckPassword(h, "correct horsE"); ok {
		t.Error("wrong password accepted")
	}
	h2, _ := HashPassword("correct horse")
	if h == h2 {
		t.Error("two hashes of one password should differ (salt)")
	}
	if _, err := HashPassword("short"); err != ErrWeakPassword {
		t.Errorf("short password: %v", err)
	}
	if _, err := CheckPassword("$bcrypt$nope", "x"); err == nil {
		t.Error("foreign hash should be an error")
	}
}

func TestNormalizeName(t *testing.T) {
	if n, err := NormalizeName("  Jair "); n != "jair" || err != nil {
		t.Errorf("got %q %v", n, err)
	}
	for _, bad := range []string{"a", "has space", "ünï", strings.Repeat("x", 33)} {
		if _, err := NormalizeName(bad); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
}

func TestLimiter(t *testing.T) {
	l := NewLimiter()
	now := time.Now()
	for range l.PerName {
		if b, _ := l.Blocked("1.2.3.4", "ana", now); b {
			t.Fatal("blocked too early")
		}
		l.Failed("1.2.3.4", "ana", now)
	}
	if b, _ := l.Blocked("1.2.3.4", "ana", now); !b {
		t.Error("should block after PerName failures")
	}
	if b, _ := l.Blocked("5.6.7.8", "ana", now); b {
		t.Error("another address should not be blocked")
	}
	if b, _ := l.Blocked("1.2.3.4", "ana", now.Add(l.Window+time.Second)); b {
		t.Error("the block should lift after the window")
	}
	// Spraying many names from one address hits the per-address limit.
	for i := range l.PerAddress {
		l.Failed("9.9.9.9", string(rune('a'+i%26))+"x", now)
	}
	if b, _ := l.Blocked("9.9.9.9", "fresh", now); !b {
		t.Error("should block an address that failed PerAddress times")
	}
}
