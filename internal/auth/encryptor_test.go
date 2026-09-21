package auth

import (
	"strings"
	"testing"
)

func TestNewEncryptor(t *testing.T) {
	tests := []struct {
		name    string
		secret  string
		wantErr bool
	}{
		{
			name:    "valid secret",
			secret:  "my-secret-key",
			wantErr: false,
		},
		{
			name:    "empty secret",
			secret:  "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enc, err := NewEncryptor(tt.secret)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewEncryptor() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && enc == nil {
				t.Error("NewEncryptor() returned nil encryptor")
			}
		})
	}
}

func TestEncryptor_EncryptDecrypt(t *testing.T) {
	enc, err := NewEncryptor("test-secret")
	if err != nil {
		t.Fatalf("Failed to create encryptor: %v", err)
	}

	plaintext := "aigw_1234567890abcdef"

	// Encrypt with default key
	encrypted, err := enc.Encrypt(plaintext, "default")
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}

	// Verify format: "keyID:base64"
	if !strings.Contains(encrypted, ":") {
		t.Error("Encrypted string should contain ':' separator")
	}

	// Decrypt
	decrypted, err := enc.Decrypt(encrypted)
	if err != nil {
		t.Fatalf("Decrypt() error = %v", err)
	}

	if decrypted != plaintext {
		t.Errorf("Decrypt() = %v, want %v", decrypted, plaintext)
	}
}

func TestEncryptor_TenantIsolation(t *testing.T) {
	enc, err := NewEncryptor("default-secret")
	if err != nil {
		t.Fatalf("Failed to create encryptor: %v", err)
	}

	// Add tenant-specific key
	if err := enc.AddTenantKey("tenant1", "tenant1-secret"); err != nil {
		t.Fatalf("AddTenantKey() error = %v", err)
	}

	plaintext := "secret-api-key"

	// Encrypt with default key
	encryptedDefault, err := enc.Encrypt(plaintext, "default")
	if err != nil {
		t.Fatalf("Encrypt(default) error = %v", err)
	}

	// Encrypt with tenant key
	encryptedTenant, err := enc.Encrypt(plaintext, "tenant1")
	if err != nil {
		t.Fatalf("Encrypt(tenant1) error = %v", err)
	}

	// Verify different ciphertexts (different keys)
	if encryptedDefault == encryptedTenant {
		t.Error("Encrypted values should differ when using different keys")
	}

	// Verify both can be decrypted correctly
	decryptedDefault, err := enc.Decrypt(encryptedDefault)
	if err != nil {
		t.Fatalf("Decrypt(default) error = %v", err)
	}
	if decryptedDefault != plaintext {
		t.Errorf("Decrypt(default) = %v, want %v", decryptedDefault, plaintext)
	}

	decryptedTenant, err := enc.Decrypt(encryptedTenant)
	if err != nil {
		t.Fatalf("Decrypt(tenant1) error = %v", err)
	}
	if decryptedTenant != plaintext {
		t.Errorf("Decrypt(tenant1) = %v, want %v", decryptedTenant, plaintext)
	}
}

func TestEncryptor_GetKeyID(t *testing.T) {
	enc, err := NewEncryptor("default-secret")
	if err != nil {
		t.Fatalf("Failed to create encryptor: %v", err)
	}

	// Add tenant key
	enc.AddTenantKey("tenant1", "tenant1-secret")

	tests := []struct {
		tenantID string
		want     string
	}{
		{"tenant1", "tenant1"},
		{"tenant2", "default"}, // No tenant key, use default
		{"", "default"},
		{"default", "default"},
	}

	for _, tt := range tests {
		t.Run(tt.tenantID, func(t *testing.T) {
			got := enc.GetKeyID(tt.tenantID)
			if got != tt.want {
				t.Errorf("GetKeyID(%v) = %v, want %v", tt.tenantID, got, tt.want)
			}
		})
	}
}

func TestEncryptor_RemoveTenantKey(t *testing.T) {
	enc, err := NewEncryptor("default-secret")
	if err != nil {
		t.Fatalf("Failed to create encryptor: %v", err)
	}

	// Add and then remove tenant key
	enc.AddTenantKey("tenant1", "tenant1-secret")
	enc.RemoveTenantKey("tenant1")

	// Should now use default key
	keyID := enc.GetKeyID("tenant1")
	if keyID != "default" {
		t.Errorf("After removal, GetKeyID() = %v, want default", keyID)
	}
}

func TestEncryptor_InvalidCiphertext(t *testing.T) {
	enc, err := NewEncryptor("test-secret")
	if err != nil {
		t.Fatalf("Failed to create encryptor: %v", err)
	}

	tests := []struct {
		name      string
		ciphertext string
		wantErr   bool
	}{
		{
			name:      "missing key ID",
			ciphertext: "just-base64-data",
			wantErr:   true,
		},
		{
			name:      "unknown key ID",
			ciphertext: "unknown-key:base64data",
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := enc.Decrypt(tt.ciphertext)
			if (err != nil) != tt.wantErr {
				t.Errorf("Decrypt() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
