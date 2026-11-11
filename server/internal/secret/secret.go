// Package secret seals values that must be stored encrypted, such as environment secrets. Each sealed
// value is bound to associated data naming where it belongs, so a ciphertext copied to another row
// fails to open.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// KeySize is the AES-256 key length.
const KeySize = 32

// version prefixes every sealed value so the format can change without guessing.
const version = "v1:"

var ErrMalformed = errors.New("malformed sealed value")

type Box struct {
	aead cipher.AEAD
}

func NewBox(key []byte) (*Box, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("secret key is %d bytes, want %d", len(key), KeySize)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create gcm: %w", err)
	}
	return &Box{aead: aead}, nil
}

func (b *Box) Seal(plaintext string, associated []byte) string {
	nonce := make([]byte, b.aead.NonceSize(), b.aead.NonceSize()+len(plaintext)+b.aead.Overhead())
	_, _ = rand.Read(nonce)
	sealed := b.aead.Seal(nonce, nonce, []byte(plaintext), associated)
	return version + base64.RawURLEncoding.EncodeToString(sealed)
}

func (b *Box) Open(sealed string, associated []byte) (string, error) {
	encoded, ok := strings.CutPrefix(sealed, version)
	if !ok {
		return "", ErrMalformed
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(raw) < b.aead.NonceSize() {
		return "", ErrMalformed
	}
	nonce, ciphertext := raw[:b.aead.NonceSize()], raw[b.aead.NonceSize():]
	plaintext, err := b.aead.Open(nil, nonce, ciphertext, associated)
	if err != nil {
		return "", fmt.Errorf("open sealed value: %w", err)
	}
	return string(plaintext), nil
}
