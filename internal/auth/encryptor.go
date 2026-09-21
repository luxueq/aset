package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"sync"
)

// Encryptor provides AES-GCM encryption and decryption.
// Supports multiple encryption keys for tenant-level isolation.
type Encryptor struct {
	mu       sync.RWMutex
	keys     map[string][]byte // keyID -> 32-byte AES key
	defaultKeyID string
}

// NewEncryptor creates a new Encryptor with a default key.
// The secret is hashed to produce a 32-byte AES key.
func NewEncryptor(defaultSecret string) (*Encryptor, error) {
	if defaultSecret == "" {
		return nil, errors.New("default encryption secret cannot be empty")
	}

	// Hash the secret to get exactly 32 bytes for AES-256
	hash := sha256.Sum256([]byte(defaultSecret))
	defaultKeyID := "default"

	return &Encryptor{
		keys:         map[string][]byte{defaultKeyID: hash[:]},
		defaultKeyID: defaultKeyID,
	}, nil
}

// AddTenantKey adds or updates an encryption key for a specific tenant.
func (e *Encryptor) AddTenantKey(tenantID, secret string) error {
	if secret == "" {
		return errors.New("tenant encryption secret cannot be empty")
	}

	hash := sha256.Sum256([]byte(secret))

	e.mu.Lock()
	defer e.mu.Unlock()
	e.keys[tenantID] = hash[:]
	return nil
}

// RemoveTenantKey removes the encryption key for a tenant.
func (e *Encryptor) RemoveTenantKey(tenantID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.keys, tenantID)
}

// GetKeyID returns the key ID to use for a tenant.
// Returns tenantID if tenant has its own key, otherwise returns default key ID.
func (e *Encryptor) GetKeyID(tenantID string) string {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if _, exists := e.keys[tenantID]; exists && tenantID != "default" {
		return tenantID
	}
	return e.defaultKeyID
}

// Encrypt encrypts plaintext using AES-256-GCM with the specified key.
// Returns base64-encoded ciphertext in format: "keyID:nonce+encrypted"
func (e *Encryptor) Encrypt(plaintext, keyID string) (string, error) {
	e.mu.RLock()
	key, exists := e.keys[keyID]
	e.mu.RUnlock()

	if !exists {
		return "", fmt.Errorf("encryption key not found: %s", keyID)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("failed to create cipher: %w", err)
	}

	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("failed to create GCM: %w", err)
	}

	// Generate random nonce
	nonce := make([]byte, aesGCM.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("failed to generate nonce: %w", err)
	}

	// Encrypt (nonce is prepended to ciphertext)
	ciphertext := aesGCM.Seal(nonce, nonce, []byte(plaintext), nil)

	// Format: "keyID:base64(nonce+ciphertext)"
	encoded := base64.StdEncoding.EncodeToString(ciphertext)
	return keyID + ":" + encoded, nil
}

// Decrypt decrypts base64-encoded ciphertext using AES-256-GCM.
// Automatically extracts key ID from the ciphertext format.
func (e *Encryptor) Decrypt(encoded string) (string, error) {
	// Parse format: "keyID:base64(nonce+ciphertext)"
	parts := splitOnce(encoded, ':')
	if len(parts) != 2 {
		return "", errors.New("invalid ciphertext format: missing key ID")
	}

	keyID := parts[0]
	ciphertextB64 := parts[1]

	e.mu.RLock()
	key, exists := e.keys[keyID]
	e.mu.RUnlock()

	if !exists {
		return "", fmt.Errorf("encryption key not found: %s", keyID)
	}

	ciphertext, err := base64.StdEncoding.DecodeString(ciphertextB64)
	if err != nil {
		return "", fmt.Errorf("failed to decode base64: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("failed to create cipher: %w", err)
	}

	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("failed to create GCM: %w", err)
	}

	nonceSize := aesGCM.NonceSize()
	if len(ciphertext) < nonceSize {
		return "", errors.New("ciphertext too short")
	}

	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := aesGCM.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("failed to decrypt: %w", err)
	}

	return string(plaintext), nil
}

// splitOnce splits a string at the first occurrence of sep.
func splitOnce(s string, sep byte) []string {
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			return []string{s[:i], s[i+1:]}
		}
	}
	return []string{s}
}
