package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
)

type SecretBox struct {
	gcm cipher.AEAD
}

func NewSecretBox(keyText string) (*SecretBox, error) {
	keyText = strings.TrimSpace(keyText)
	key := []byte(keyText)

	if decoded, err := base64.StdEncoding.DecodeString(keyText); err == nil && len(decoded) == 32 {
		key = decoded
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("encryption key must be 32 bytes or base64-encoded 32 bytes, got %d bytes", len(key))
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &SecretBox{gcm: gcm}, nil
}

func (b *SecretBox) Encrypt(plaintext string) (ciphertext string, nonce string, err error) {
	if b == nil {
		return "", "", errors.New("secretbox is nil")
	}
	n := make([]byte, b.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, n); err != nil {
		return "", "", err
	}
	sealed := b.gcm.Seal(nil, n, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(sealed), base64.StdEncoding.EncodeToString(n), nil
}

func (b *SecretBox) Decrypt(ciphertext string, nonce string) (string, error) {
	if b == nil {
		return "", errors.New("secretbox is nil")
	}
	c, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", err
	}
	n, err := base64.StdEncoding.DecodeString(nonce)
	if err != nil {
		return "", err
	}
	plain, err := b.gcm.Open(nil, n, c, nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func MaskSecret(secret string) string {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return ""
	}
	rs := []rune(secret)
	if len(rs) <= 4 {
		return "****"
	}
	if len(rs) <= 10 {
		return string(rs[:2]) + "****" + string(rs[len(rs)-2:])
	}
	return string(rs[:3]) + "****" + string(rs[len(rs)-4:])
}
