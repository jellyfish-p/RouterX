package common

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"strings"
)

const encryptedSecretPrefix = "enc:v1:"

// IsEncryptedSecret reports whether value is a complete RouterX encrypted
// secret payload. It is stricter than ContainsEncryptedSecret, which is useful
// for scanning serialized JSON before parsing it.
func IsEncryptedSecret(value string) bool {
	return strings.HasPrefix(value, encryptedSecretPrefix)
}

// ContainsEncryptedSecret reports whether a plain string or serialized JSON
// payload contains at least one RouterX encrypted secret marker.
func ContainsEncryptedSecret(value string) bool {
	return strings.Contains(value, encryptedSecretPrefix)
}

func EncryptSecret(plain string) (string, error) {
	key := strings.TrimSpace(os.Getenv("ENCRYPTION_KEY"))
	if key == "" || plain == "" || IsEncryptedSecret(plain) {
		return plain, nil
	}

	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nil, nonce, []byte(plain), nil)
	return encryptedSecretPrefix +
		base64.RawURLEncoding.EncodeToString(nonce) + ":" +
		base64.RawURLEncoding.EncodeToString(ciphertext), nil
}

func DecryptSecret(value string) (string, error) {
	if !IsEncryptedSecret(value) {
		return value, nil
	}
	key := strings.TrimSpace(os.Getenv("ENCRYPTION_KEY"))
	if key == "" {
		return "", errors.New("ENCRYPTION_KEY is required to decrypt secret")
	}

	parts := strings.Split(strings.TrimPrefix(value, encryptedSecretPrefix), ":")
	if len(parts) != 2 {
		return "", errors.New("invalid encrypted secret format")
	}
	nonce, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", err
	}
	ciphertext, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", err
	}
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func RedactSecret(value string) string {
	if value == "" {
		return ""
	}
	if len(value) <= 8 {
		return "***"
	}
	return value[:4] + "..." + value[len(value)-4:]
}

func newGCM(key string) (cipher.AEAD, error) {
	sum := sha256.Sum256([]byte(key))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
