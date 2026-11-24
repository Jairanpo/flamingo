package ticket

import (
	"strings"
	"testing"
	"time"
)

func TestTicket(t *testing.T) {
	key := KeyOf("the-agent-token")
	now := time.Unix(1_800_000_000, 0)
	tk := Sign(key, Claims{Device: "laptop", User: 7, Expiry: now.Add(TTL).Unix()})

	c, err := Verify(key, "laptop", tk, now)
	if err != nil || c.User != 7 {
		t.Fatalf("good ticket: %v %+v", err, c)
	}
	if strings.ContainsAny(tk, "+/=") {
		t.Errorf("not URL safe: %s", tk)
	}

	cases := []struct {
		name   string
		key    []byte
		device string
		tk     string
		at     time.Time
		want   error
	}{
		{"other key", KeyOf("another-token"), "laptop", tk, now, ErrSignature},
		{"other computer", key, "desk", tk, now, ErrDevice},
		{"expired", key, "laptop", tk, now.Add(TTL), ErrExpired},
		{"no dot", key, "laptop", "abc", now, ErrMalformed},
		{"empty", key, "laptop", "", now, ErrMalformed},
		// Someone else's claims under this ticket's signature.
		{"tampered", key, "laptop", strings.Split(Sign(key, Claims{Device: "laptop", User: 1, Expiry: now.Add(TTL).Unix()}), ".")[0] +
			"." + strings.Split(tk, ".")[1], now, ErrSignature},
	}
	for _, c := range cases {
		if _, err := Verify(c.key, c.device, c.tk, c.at); err != c.want {
			t.Errorf("%s: got %v, want %v", c.name, err, c.want)
		}
	}
}
