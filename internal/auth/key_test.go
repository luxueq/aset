package auth

import (
	"testing"
	"time"
)

func TestKeyService_Create(t *testing.T) {
	keyService, err := NewKeyService("test-secret")
	if err != nil {
		t.Fatalf("Failed to create key service: %v", err)
	}

	req := CreateKeyRequest{
		TenantID:    "tenant1",
		Name:        "Test Key",
		Permissions: []string{"chat", "embeddings"},
	}

	resp, err := keyService.Create(req)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	// Verify response
	if resp.ID == "" {
		t.Error("Create() returned empty ID")
	}
	if resp.Key == "" {
		t.Error("Create() returned empty key")
	}
	if resp.TenantID != req.TenantID {
		t.Errorf("Create() tenant_id = %v, want %v", resp.TenantID, req.TenantID)
	}
	if resp.Name != req.Name {
		t.Errorf("Create() name = %v, want %v", resp.Name, req.Name)
	}
	if !startswith(resp.Key, "aigw_") {
		t.Errorf("Create() key should start with 'aigw_', got %v", resp.Key)
	}
}

func TestKeyService_Validate(t *testing.T) {
	keyService, err := NewKeyService("test-secret")
	if err != nil {
		t.Fatalf("Failed to create key service: %v", err)
	}

	// Create a key
	req := CreateKeyRequest{
		TenantID:    "tenant1",
		Name:        "Test Key",
		Permissions: []string{"chat"},
	}
	resp, _ := keyService.Create(req)

	// Validate the key
	apiKey, err := keyService.Validate(resp.Key)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	if apiKey.ID != resp.ID {
		t.Errorf("Validate() ID = %v, want %v", apiKey.ID, resp.ID)
	}
	if apiKey.TenantID != req.TenantID {
		t.Errorf("Validate() TenantID = %v, want %v", apiKey.TenantID, req.TenantID)
	}
	if apiKey.LastUsedAt == nil {
		t.Error("Validate() should update LastUsedAt")
	}
}

func TestKeyService_ValidateErrors(t *testing.T) {
	keyService, err := NewKeyService("test-secret")
	if err != nil {
		t.Fatalf("Failed to create key service: %v", err)
	}

	tests := []struct {
		name    string
		key     string
		wantErr error
	}{
		{
			name:    "non-existent key",
			key:     "aigw_nonexistent",
			wantErr: ErrKeyNotFound,
		},
		{
			name:    "empty key",
			key:     "",
			wantErr: ErrKeyNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := keyService.Validate(tt.key)
			if err != tt.wantErr {
				t.Errorf("Validate() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestKeyService_Revoke(t *testing.T) {
	keyService, err := NewKeyService("test-secret")
	if err != nil {
		t.Fatalf("Failed to create key service: %v", err)
	}

	// Create a key
	req := CreateKeyRequest{TenantID: "tenant1", Name: "Test Key"}
	resp, _ := keyService.Create(req)

	// Revoke the key
	err = keyService.Revoke("tenant1", resp.ID)
	if err != nil {
		t.Fatalf("Revoke() error = %v", err)
	}

	// Try to validate revoked key
	_, err = keyService.Validate(resp.Key)
	if err != ErrKeyRevoked {
		t.Errorf("Validate() after revoke: error = %v, want %v", err, ErrKeyRevoked)
	}
}

func TestKeyService_Expired(t *testing.T) {
	keyService, err := NewKeyService("test-secret")
	if err != nil {
		t.Fatalf("Failed to create key service: %v", err)
	}

	// Create an expired key
	expiredTime := time.Now().Add(-1 * time.Hour)
	req := CreateKeyRequest{
		TenantID:  "tenant1",
		Name:      "Expired Key",
		ExpiresAt: &expiredTime,
	}
	resp, _ := keyService.Create(req)

	// Try to validate expired key
	_, err = keyService.Validate(resp.Key)
	if err != ErrKeyExpired {
		t.Errorf("Validate() expired key: error = %v, want %v", err, ErrKeyExpired)
	}
}

func TestKeyService_TenantIsolation(t *testing.T) {
	keyService, err := NewKeyService("test-secret")
	if err != nil {
		t.Fatalf("Failed to create key service: %v", err)
	}

	// Create keys for different tenants
	req1 := CreateKeyRequest{TenantID: "tenant1", Name: "Key 1"}
	resp1, _ := keyService.Create(req1)

	req2 := CreateKeyRequest{TenantID: "tenant2", Name: "Key 2"}
	resp2, _ := keyService.Create(req2)

	// Tenant1 should not be able to access tenant2's key
	_, err = keyService.Get("tenant1", resp2.ID)
	if err != ErrKeyNotFound {
		t.Errorf("Get() cross-tenant: error = %v, want %v", err, ErrKeyNotFound)
	}

	// Tenant2 should not be able to revoke tenant1's key
	err = keyService.Revoke("tenant2", resp1.ID)
	if err != ErrKeyNotFound {
		t.Errorf("Revoke() cross-tenant: error = %v, want %v", err, ErrKeyNotFound)
	}

	// Tenant2 should not be able to delete tenant1's key
	err = keyService.Delete("tenant2", resp1.ID)
	if err != ErrKeyNotFound {
		t.Errorf("Delete() cross-tenant: error = %v, want %v", err, ErrKeyNotFound)
	}
}

func TestKeyService_List(t *testing.T) {
	keyService, err := NewKeyService("test-secret")
	if err != nil {
		t.Fatalf("Failed to create key service: %v", err)
	}

	// Create multiple keys for tenant1
	keyService.Create(CreateKeyRequest{TenantID: "tenant1", Name: "Key 1"})
	keyService.Create(CreateKeyRequest{TenantID: "tenant1", Name: "Key 2"})
	keyService.Create(CreateKeyRequest{TenantID: "tenant2", Name: "Key 3"})

	// List tenant1's keys
	keys, err := keyService.List("tenant1")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	if len(keys) != 2 {
		t.Errorf("List() returned %d keys, want 2", len(keys))
	}

	// Verify all keys belong to tenant1
	for _, k := range keys {
		if k.TenantID != "tenant1" {
			t.Errorf("List() returned key with tenant_id = %v, want tenant1", k.TenantID)
		}
	}
}

func TestKeyService_Delete(t *testing.T) {
	keyService, err := NewKeyService("test-secret")
	if err != nil {
		t.Fatalf("Failed to create key service: %v", err)
	}

	// Create a key
	req := CreateKeyRequest{TenantID: "tenant1", Name: "Test Key"}
	resp, _ := keyService.Create(req)

	// Delete the key
	err = keyService.Delete("tenant1", resp.ID)
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	// Verify key is gone
	_, err = keyService.Validate(resp.Key)
	if err != ErrKeyNotFound {
		t.Errorf("Validate() after delete: error = %v, want %v", err, ErrKeyNotFound)
	}
}

func TestKeyService_HasPermission(t *testing.T) {
	key := &APIKey{
		Permissions: []string{"chat", "embeddings"},
	}

	tests := []struct {
		permission string
		want       bool
	}{
		{"chat", true},
		{"embeddings", true},
		{"images", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.permission, func(t *testing.T) {
			got := key.HasPermission(tt.permission)
			if got != tt.want {
				t.Errorf("HasPermission(%v) = %v, want %v", tt.permission, got, tt.want)
			}
		})
	}
}

func TestKeyService_TenantEncryption(t *testing.T) {
	keyService, err := NewKeyService("default-secret")
	if err != nil {
		t.Fatalf("Failed to create key service: %v", err)
	}

	// Add tenant-specific encryption key
	keyService.GetEncryptor().AddTenantKey("tenant1", "tenant1-secret")

	// Create keys for different tenants
	req1 := CreateKeyRequest{TenantID: "tenant1", Name: "Tenant1 Key"}
	resp1, _ := keyService.Create(req1)

	req2 := CreateKeyRequest{TenantID: "tenant2", Name: "Tenant2 Key"}
	resp2, _ := keyService.Create(req2)

	// Get the API keys to check encryption key IDs
	apiKey1, _ := keyService.Get("tenant1", resp1.ID)
	apiKey2, _ := keyService.Get("tenant2", resp2.ID)

	// Verify different encryption key IDs
	if apiKey1.EncryptionKeyID != "tenant1" {
		t.Errorf("Tenant1 key should use tenant1 encryption key, got %v", apiKey1.EncryptionKeyID)
	}
	if apiKey2.EncryptionKeyID != "default" {
		t.Errorf("Tenant2 key should use default encryption key, got %v", apiKey2.EncryptionKeyID)
	}

	// Both keys should still validate correctly
	_, err = keyService.Validate(resp1.Key)
	if err != nil {
		t.Errorf("Validate(tenant1 key) error = %v", err)
	}

	_, err = keyService.Validate(resp2.Key)
	if err != nil {
		t.Errorf("Validate(tenant2 key) error = %v", err)
	}
}

// Helper function
func startswith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
