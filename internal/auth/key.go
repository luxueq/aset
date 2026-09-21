package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// APIKey represents an API key with its metadata and configuration.
type APIKey struct {
	ID              string           `json:"id"`
	KeyHash         string           `json:"-"` // SHA-256 hash for lookup (not exposed)
	KeyPrefix       string           `json:"key_prefix"`
	KeyEncrypted    string           `json:"-"` // AES-GCM encrypted full key (not exposed)
	EncryptionKeyID string           `json:"-"` // ID of the encryption key used (tenant-level isolation)
	TenantID        string           `json:"tenant_id"`
	Name            string           `json:"name"`
	Permissions     []string         `json:"permissions"` // ["chat", "embeddings", "images"]
	RateLimit       *RateLimitConfig `json:"rate_limit,omitempty"`
	Quota           *QuotaConfig     `json:"quota,omitempty"`
	CreatedAt       time.Time        `json:"created_at"`
	ExpiresAt       *time.Time       `json:"expires_at,omitempty"`
	Revoked         bool             `json:"revoked"`
	LastUsedAt      *time.Time       `json:"last_used_at,omitempty"`
}

// RateLimitConfig holds per-key rate limit settings.
type RateLimitConfig struct {
	TokensPerMinute int `json:"tokens_per_minute"`
	TokensPerDay    int `json:"tokens_per_day"`
	MaxConcurrent   int `json:"max_concurrent"`
}

// QuotaConfig holds per-key quota settings.
type QuotaConfig struct {
	MonthlyTokens int64   `json:"monthly_tokens"`
	MonthlyBudget float64 `json:"monthly_budget"` // in USD
}

// CreateKeyRequest represents a request to create a new API key.
type CreateKeyRequest struct {
	TenantID    string           `json:"tenant_id"`
	Name        string           `json:"name"`
	Permissions []string         `json:"permissions"`
	RateLimit   *RateLimitConfig `json:"rate_limit,omitempty"`
	Quota       *QuotaConfig     `json:"quota,omitempty"`
	ExpiresAt   *time.Time       `json:"expires_at,omitempty"`
}

// CreateKeyResponse represents the response when creating a new API key.
type CreateKeyResponse struct {
	ID        string     `json:"id"`
	Key       string     `json:"key"` // Plaintext key (only returned once)
	KeyPrefix string     `json:"key_prefix"`
	TenantID  string     `json:"tenant_id"`
	Name      string     `json:"name"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// KeyService provides API key management operations.
type KeyService struct {
	mu        sync.RWMutex
	keys      map[string]*APIKey // keyHash -> APIKey
	encryptor *Encryptor
}

// Common errors
var (
	ErrKeyNotFound  = errors.New("api key not found")
	ErrKeyRevoked   = errors.New("api key has been revoked")
	ErrKeyExpired   = errors.New("api key has expired")
	ErrInvalidKey   = errors.New("invalid api key")
	ErrDuplicateKey = errors.New("api key already exists")
)

// NewKeyService creates a new KeyService with the given encryption secret.
func NewKeyService(encryptionSecret string) (*KeyService, error) {
	encryptor, err := NewEncryptor(encryptionSecret)
	if err != nil {
		return nil, fmt.Errorf("failed to create encryptor: %w", err)
	}

	return &KeyService{
		keys:      make(map[string]*APIKey),
		encryptor: encryptor,
	}, nil
}

// Create generates a new API key and stores it.
// Returns the plaintext key (only time it's shown to the user).
func (s *KeyService) Create(req CreateKeyRequest) (*CreateKeyResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Generate random key: "aigw_" + 32 bytes hex
	plaintextKey, err := generateKey()
	if err != nil {
		return nil, fmt.Errorf("failed to generate key: %w", err)
	}

	// Compute hash for lookup
	keyHash := hashKey(plaintextKey)

	// Check for duplicates
	if _, exists := s.keys[keyHash]; exists {
		return nil, ErrDuplicateKey
	}

	// Get encryption key ID for this tenant (tenant-level isolation)
	encryptionKeyID := s.encryptor.GetKeyID(req.TenantID)

	// Encrypt the full key with tenant-specific key
	encryptedKey, err := s.encryptor.Encrypt(plaintextKey, encryptionKeyID)
	if err != nil {
		return nil, fmt.Errorf("failed to encrypt key: %w", err)
	}

	// Extract prefix (first 13 chars: "aigw_" + 8 chars + "***")
	keyPrefix := plaintextKey[:13] + "***"

	// Create API key record
	apiKey := &APIKey{
		ID:              generateID(),
		KeyHash:         keyHash,
		KeyPrefix:       keyPrefix,
		KeyEncrypted:    encryptedKey,
		EncryptionKeyID: encryptionKeyID,
		TenantID:        req.TenantID,
		Name:            req.Name,
		Permissions:     req.Permissions,
		RateLimit:       req.RateLimit,
		Quota:           req.Quota,
		CreatedAt:       time.Now(),
		ExpiresAt:       req.ExpiresAt,
		Revoked:         false,
	}

	// Store
	s.keys[keyHash] = apiKey

	return &CreateKeyResponse{
		ID:        apiKey.ID,
		Key:       plaintextKey,
		KeyPrefix: keyPrefix,
		TenantID:  apiKey.TenantID,
		Name:      apiKey.Name,
		CreatedAt: apiKey.CreatedAt,
		ExpiresAt: apiKey.ExpiresAt,
	}, nil
}

// Validate checks if an API key is valid and returns the key metadata.
func (s *KeyService) Validate(plaintextKey string) (*APIKey, error) {
	// Use read lock for lookup
	s.mu.RLock()
	keyHash := hashKey(plaintextKey)
	apiKey, exists := s.keys[keyHash]
	s.mu.RUnlock()

	if !exists {
		return nil, ErrKeyNotFound
	}

	// Check if revoked
	if apiKey.Revoked {
		return nil, ErrKeyRevoked
	}

	// Check if expired
	if apiKey.ExpiresAt != nil && time.Now().After(*apiKey.ExpiresAt) {
		return nil, ErrKeyExpired
	}

	// Update last used timestamp (requires write lock)
	s.mu.Lock()
	now := time.Now()
	apiKey.LastUsedAt = &now
	s.mu.Unlock()

	return apiKey, nil
}

// Get retrieves an API key by ID for a specific tenant (tenant-scoped).
// Returns ErrKeyNotFound if the key doesn't exist or doesn't belong to the tenant.
func (s *KeyService) Get(tenantID, id string) (*APIKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, apiKey := range s.keys {
		if apiKey.ID == id {
			// Tenant isolation: key must belong to the requesting tenant
			if apiKey.TenantID != tenantID {
				return nil, ErrKeyNotFound // Don't reveal the key exists
			}
			return apiKey, nil
		}
	}

	return nil, ErrKeyNotFound
}

// List returns all API keys for a tenant (metadata only, no plaintext keys).
func (s *KeyService) List(tenantID string) ([]*APIKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var keys []*APIKey
	for _, apiKey := range s.keys {
		if apiKey.TenantID == tenantID {
			keys = append(keys, apiKey)
		}
	}

	return keys, nil
}

// Revoke marks an API key as revoked (tenant-scoped).
func (s *KeyService) Revoke(tenantID, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, apiKey := range s.keys {
		if apiKey.ID == id {
			// Tenant isolation check
			if apiKey.TenantID != tenantID {
				return ErrKeyNotFound
			}
			apiKey.Revoked = true
			return nil
		}
	}

	return ErrKeyNotFound
}

// Delete permanently removes an API key (tenant-scoped).
func (s *KeyService) Delete(tenantID, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for hash, apiKey := range s.keys {
		if apiKey.ID == id {
			// Tenant isolation check
			if apiKey.TenantID != tenantID {
				return ErrKeyNotFound
			}
			delete(s.keys, hash)
			return nil
		}
	}

	return ErrKeyNotFound
}

// HasPermission checks if an API key has a specific permission.
func (k *APIKey) HasPermission(permission string) bool {
	for _, p := range k.Permissions {
		if p == permission {
			return true
		}
	}
	return false
}

// RotateTenantKey rotates the encryption key for a tenant.
// All existing API keys for the tenant will be re-encrypted with the new key.
func (s *KeyService) RotateTenantKey(tenantID, newSecret string) error {
	// Add the new key first
	if err := s.encryptor.AddTenantKey(tenantID, newSecret); err != nil {
		return fmt.Errorf("failed to add new tenant key: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Re-encrypt all keys for this tenant
	for _, apiKey := range s.keys {
		if apiKey.TenantID == tenantID {
			// Decrypt with old key
			plaintext, err := s.encryptor.Decrypt(apiKey.KeyEncrypted)
			if err != nil {
				// If decryption fails, the key might already be using the new key
				// Skip this key
				continue
			}

			// Re-encrypt with new key
			newKeyID := s.encryptor.GetKeyID(tenantID)
			encrypted, err := s.encryptor.Encrypt(plaintext, newKeyID)
			if err != nil {
				return fmt.Errorf("failed to re-encrypt key %s: %w", apiKey.ID, err)
			}

			apiKey.KeyEncrypted = encrypted
			apiKey.EncryptionKeyID = newKeyID
		}
	}

	return nil
}

// GetEncryptor returns the encryptor instance (for testing and admin operations).
func (s *KeyService) GetEncryptor() *Encryptor {
	return s.encryptor
}

// --- Helper functions ---

// generateKey creates a new random API key.
func generateKey() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return "aigw_" + hex.EncodeToString(bytes), nil
}

// hashKey computes SHA-256 hash of a key.
func hashKey(key string) string {
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:])
}

// generateID creates a unique ID for an API key using UUID v4.
func generateID() string {
	return uuid.New().String()
}

// ExtractKeyFromHeader extracts the bearer token from Authorization header.
func ExtractKeyFromHeader(authHeader string) (string, error) {
	if authHeader == "" {
		return "", ErrInvalidKey
	}

	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return "", ErrInvalidKey
	}

	return strings.TrimSpace(parts[1]), nil
}
