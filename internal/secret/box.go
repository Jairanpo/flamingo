// Package secret seals small values (Toggl tokens) before they reach the
// database, with a key that lives only in a file on the server. A copy of
// the database alone is then not enough to use anyone's token.
package secret

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
)

// ErrNoKey: auth.secret_key_file is not set, so nothing can be sealed.
var ErrNoKey = errors.New("no secret key: set auth.secret_key_file (e.g. `openssl rand -base64 32 > key`)")

// Box seals and opens values with AES-256-GCM.
type Box struct{ aead cipher.AEAD }

// Load reads the key file. An empty path gives a nil Box, which refuses
// everything with ErrNoKey.
func Load(path string) (*Box, error) {
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading secret key: %w", err)
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) < 16 {
		return nil, fmt.Errorf("secret key in %s is too short (want 32 random bytes, e.g. openssl rand -base64 32)", path)
	}
	return New(raw), nil
}

// New makes a Box from any key material; it is hashed to 32 bytes.
func New(key []byte) *Box {
	sum := sha256.Sum256(key)
	block, _ := aes.NewCipher(sum[:]) // 32 bytes is always a valid AES key
	aead, _ := cipher.NewGCM(block)
	return &Box{aead: aead}
}

// Seal returns nonce + ciphertext.
func (b *Box) Seal(plain []byte) ([]byte, error) {
	if b == nil {
		return nil, ErrNoKey
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return b.aead.Seal(nonce, nonce, plain, nil), nil
}

// Open reverses Seal. A value sealed under another key fails.
func (b *Box) Open(sealed []byte) ([]byte, error) {
	if b == nil {
		return nil, ErrNoKey
	}
	n := b.aead.NonceSize()
	if len(sealed) < n {
		return nil, errors.New("sealed value is too short")
	}
	plain, err := b.aead.Open(nil, sealed[:n], sealed[n:], nil)
	if err != nil {
		return nil, errors.New("sealed value does not open with this key (was the key file changed?)")
	}
	return plain, nil
}
