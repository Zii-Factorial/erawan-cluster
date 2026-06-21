package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
)

// Cipher wraps AES-256-GCM. Key must be 32 bytes supplied as 64 hex chars.
type Cipher struct {
	aead cipher.AEAD
}

/**
 * NewCipher.
 *
 * Params:
 *   hexKey string - the hexKey string
 *
 * Returns:
 *   *Cipher - the resulting *Cipher
 *   error - error value; non-nil when the operation fails
 */
func NewCipher(hexKey string) (*Cipher, error) {
	key, err := hex.DecodeString(hexKey)
	if err != nil {
		return nil, fmt.Errorf("encryption key must be hex-encoded: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("encryption key must be 32 bytes (64 hex chars), got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create aes cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create gcm: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

/**
 * Encrypt encrypts plaintext and returns base64url(nonce + ciphertext).
 *
 * Receiver:
 *   c *Cipher - pointer receiver; the method may mutate this Cipher instance
 *
 * Params:
 *   plaintext []byte - the plaintext bytes
 *
 * Returns:
 *   string - the resulting string
 *   error - error value; non-nil when the operation fails
 */
func (c *Cipher) Encrypt(plaintext []byte) (string, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}
	sealed := c.aead.Seal(nonce, nonce, plaintext, nil)
	return base64.URLEncoding.EncodeToString(sealed), nil
}

/**
 * Decrypt decrypts a base64url(nonce + ciphertext) string produced by Encrypt.
 *
 * Receiver:
 *   c *Cipher - pointer receiver; the method may mutate this Cipher instance
 *
 * Params:
 *   encoded string - the encoded string
 *
 * Returns:
 *   []byte - the resulting bytes
 *   error - error value; non-nil when the operation fails
 */
func (c *Cipher) Decrypt(encoded string) ([]byte, error) {
	data, err := base64.URLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode ciphertext: %w", err)
	}
	nonceSize := c.aead.NonceSize()
	if len(data) < nonceSize+1 {
		return nil, fmt.Errorf("ciphertext too short")
	}
	plaintext, err := c.aead.Open(nil, data[:nonceSize], data[nonceSize:], nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}
	return plaintext, nil
}
