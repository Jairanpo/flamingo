package secret

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSealOpen(t *testing.T) {
	b := New([]byte("a key long enough to use"))
	s1, err := b.Seal([]byte("token"))
	if err != nil {
		t.Fatal(err)
	}
	s2, _ := b.Seal([]byte("token"))
	if string(s1) == string(s2) {
		t.Fatal("two seals of one value should differ (fresh nonce)")
	}
	got, err := b.Open(s1)
	if err != nil || string(got) != "token" {
		t.Fatalf("open = %q, %v", got, err)
	}
	if _, err := New([]byte("another key entirely")).Open(s1); err == nil {
		t.Fatal("a different key must not open it")
	}
	s1[len(s1)-1] ^= 1
	if _, err := b.Open(s1); err == nil {
		t.Fatal("a changed value must not open")
	}
}

func TestLoad(t *testing.T) {
	if b, err := Load(""); b != nil || err != nil {
		t.Fatalf("no path = %v, %v", b, err)
	}
	var none *Box
	if _, err := none.Seal([]byte("x")); !errors.Is(err, ErrNoKey) {
		t.Fatalf("nil box seal = %v", err)
	}
	dir := t.TempDir()
	short := filepath.Join(dir, "short")
	os.WriteFile(short, []byte("abc\n"), 0o600)
	if _, err := Load(short); err == nil {
		t.Fatal("short key accepted")
	}
	good := filepath.Join(dir, "good")
	os.WriteFile(good, []byte("c29tZSByYW5kb20ga2V5IG1hdGVyaWFsIGhlcmU=\n"), 0o600)
	b, err := Load(good)
	if err != nil {
		t.Fatal(err)
	}
	// Trailing newline is ignored: the same key without it opens the value.
	s, _ := b.Seal([]byte("v"))
	if got, err := New([]byte("c29tZSByYW5kb20ga2V5IG1hdGVyaWFsIGhlcmU=")).Open(s); err != nil || string(got) != "v" {
		t.Fatalf("open = %q, %v", got, err)
	}
}
