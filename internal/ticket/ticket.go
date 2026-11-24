// Package ticket is the short-lived pass a Flamingo page shows a computer's
// agent. The browser never sends its server login to the agent (another
// address), so the server writes the page a ticket instead: which user, for
// which computer, until when, signed with that computer's key.
//
// The key is sha256 of the agent's token. The server keeps only that hash,
// and the agent can work it out from the token it holds, so the agent checks
// a ticket on its own, without asking the server.
package ticket

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// TTL is how long a ticket works. The page asks for a new one well before.
const TTL = 15 * time.Minute

// Claims is what a ticket says.
type Claims struct {
	Device string `json:"d"`
	User   int32  `json:"u"`
	Expiry int64  `json:"e"` // unix seconds
}

var (
	ErrMalformed = errors.New("ticket: malformed")
	ErrSignature = errors.New("ticket: bad signature")
	ErrExpired   = errors.New("ticket: expired")
	ErrDevice    = errors.New("ticket: for another computer")
)

// KeyOf turns an agent's token into the key its tickets are signed with,
// the same bytes the server stores as devi_token_hash.
func KeyOf(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

var b64 = base64.RawURLEncoding

func mac(key []byte, payload string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(payload))
	return m.Sum(nil)
}

// Sign writes a ticket: base64(claims) "." base64(hmac). It only uses
// URL-safe characters, so it can ride in a video's address.
func Sign(key []byte, c Claims) string {
	raw, _ := json.Marshal(c)
	payload := b64.EncodeToString(raw)
	return payload + "." + b64.EncodeToString(mac(key, payload))
}

// Verify checks a ticket's signature, its computer and its time.
func Verify(key []byte, device, t string, now time.Time) (Claims, error) {
	var c Claims
	payload, sig, ok := strings.Cut(t, ".")
	if !ok || payload == "" || sig == "" {
		return c, ErrMalformed
	}
	got, err := b64.DecodeString(sig)
	if err != nil {
		return c, ErrMalformed
	}
	if !hmac.Equal(got, mac(key, payload)) {
		return c, ErrSignature
	}
	raw, err := b64.DecodeString(payload)
	if err != nil || json.Unmarshal(raw, &c) != nil {
		return c, ErrMalformed
	}
	if c.Device != device {
		return c, ErrDevice
	}
	if now.Unix() >= c.Expiry {
		return c, ErrExpired
	}
	return c, nil
}
