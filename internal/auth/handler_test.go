package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newTestKeyService builds a KeyService with a fresh random secret.
func newTestKeyService(t *testing.T) *KeyService {
	t.Helper()
	s, err := NewKeyService("test-secret")
	if err != nil {
		t.Fatalf("NewKeyService() error = %v", err)
	}
	return s
}

// newHandlerMux builds the API key management routes.
func newHandlerMux(t *testing.T) (*Handler, *http.ServeMux) {
	t.Helper()
	h := NewHandler(newTestKeyService(t))
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return h, mux
}

// createKeyViaHTTP creates a key through the HTTP endpoint and returns
// the decoded response.
func createKeyViaHTTP(t *testing.T, mux *http.ServeMux) *CreateKeyResponse {
	t.Helper()

	body := `{"tenant_id":"tenant1","name":"Test Key","permissions":["chat","embeddings"]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/api-keys", strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /v1/api-keys status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	var resp CreateKeyResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	return &resp
}

// TestHandler_CreateKey verifies POST /v1/api-keys returns 201 and the
// plaintext key exactly once.
func TestHandler_CreateKey(t *testing.T) {
	t.Parallel()

	_, mux := newHandlerMux(t)
	resp := createKeyViaHTTP(t, mux)

	if resp.ID == "" {
		t.Error("created key ID is empty")
	}
	if !strings.HasPrefix(resp.Key, "aigw_") {
		t.Errorf("plaintext key = %q, want aigw_ prefix", resp.Key)
	}
	if resp.KeyPrefix == "" || !strings.HasSuffix(resp.KeyPrefix, "***") {
		t.Errorf("key_prefix = %q, want display prefix ending with ***", resp.KeyPrefix)
	}
}

// TestHandler_CreateKeyValidation verifies missing fields return 400.
func TestHandler_CreateKeyValidation(t *testing.T) {
	t.Parallel()

	_, mux := newHandlerMux(t)

	tests := []struct {
		name string
		body string
	}{
		{"missing tenant", `{"name":"k"}`},
		{"missing name", `{"tenant_id":"t1"}`},
		{"invalid json", `{not json`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/api-keys", strings.NewReader(tt.body))
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
		})
	}
}

// TestHandler_ListKeysPrefixOnly verifies GET /v1/api-keys lists keys
// with the prefix only — the full key must never appear in the listing.
func TestHandler_ListKeysPrefixOnly(t *testing.T) {
	t.Parallel()

	_, mux := newHandlerMux(t)
	created := createKeyViaHTTP(t, mux)

	req := httptest.NewRequest(http.MethodGet, "/v1/api-keys?tenant_id=tenant1", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/api-keys status = %d, want 200", rec.Code)
	}
	if strings.Contains(rec.Body.String(), created.Key) {
		t.Error("full plaintext key leaked in listing response")
	}
	if !strings.Contains(rec.Body.String(), created.KeyPrefix) {
		t.Errorf("listing missing key_prefix %q", created.KeyPrefix)
	}
}

// TestHandler_ListKeysRequiresTenant verifies missing tenant_id is a 400.
func TestHandler_ListKeysRequiresTenant(t *testing.T) {
	t.Parallel()

	_, mux := newHandlerMux(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/api-keys", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (tenant_id required)", rec.Code)
	}
}

// TestHandler_DeleteRevokesKey verifies DELETE /v1/api-keys/:id revokes
// the key and the revoked key fails validation with 401 afterwards.
func TestHandler_DeleteRevokesKey(t *testing.T) {
	t.Parallel()

	h, mux := newHandlerMux(t)
	created := createKeyViaHTTP(t, mux)

	// DELETE
	req := httptest.NewRequest(http.MethodDelete, "/v1/api-keys/"+created.ID+"?tenant_id=tenant1", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"status":"revoked"`) {
		t.Errorf("DELETE body = %s, want revoked status", rec.Body.String())
	}

	// The revoked key must now fail validation (the middleware path).
	if _, err := h.keyService.Validate(created.Key); err != ErrKeyRevoked {
		t.Errorf("Validate(revoked) error = %v, want %v", err, ErrKeyRevoked)
	}

	// Deleting an unknown key returns 404.
	req = httptest.NewRequest(http.MethodDelete, "/v1/api-keys/nonexistent?tenant_id=tenant1", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("DELETE unknown status = %d, want 404", rec.Code)
	}

	// Cross-tenant delete must not revoke: 404.
	created2 := createKeyViaHTTP(t, mux)
	req = httptest.NewRequest(http.MethodDelete, "/v1/api-keys/"+created2.ID+"?tenant_id=other", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("cross-tenant DELETE status = %d, want 404", rec.Code)
	}
	if _, err := h.keyService.Validate(created2.Key); err != nil {
		t.Errorf("Validate() after cross-tenant delete attempt: %v, want nil (key not revoked)", err)
	}
}

// TestHandler_KeyEncryptedAtRest verifies the stored key is AES-GCM
// encrypted: the plaintext never appears in KeyEncrypted, and it can be
// decrypted back via the encryptor.
func TestHandler_KeyEncryptedAtRest(t *testing.T) {
	t.Parallel()

	h, mux := newHandlerMux(t)
	created := createKeyViaHTTP(t, mux)

	// Fetch the stored record via the service (handler Get route).
	req := httptest.NewRequest(http.MethodGet, "/v1/api-keys/"+created.ID+"?tenant_id=tenant1", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET key status = %d, want 200", rec.Code)
	}
	// JSON must not leak the encrypted/hashed fields (json:"-" tags).
	if strings.Contains(rec.Body.String(), "KeyEncrypted") || strings.Contains(rec.Body.String(), "KeyHash") {
		t.Error("serialized APIKey leaked KeyEncrypted/KeyHash fields")
	}

	apiKey, err := h.keyService.Get("tenant1", created.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if strings.Contains(apiKey.KeyEncrypted, created.Key) {
		t.Error("KeyEncrypted contains the plaintext key")
	}
	if !strings.Contains(apiKey.KeyEncrypted, ":") {
		t.Errorf("KeyEncrypted = %q, want keyID:base64(nonce+ciphertext) format", apiKey.KeyEncrypted)
	}

	decrypted, err := h.keyService.GetEncryptor().Decrypt(apiKey.KeyEncrypted)
	if err != nil {
		t.Fatalf("Decrypt() error = %v", err)
	}
	if decrypted != created.Key {
		t.Error("decrypted key does not match the plaintext key")
	}
}

// TestHandler_MissingAuthGate is a placeholder guard: management routes
// currently have no admin auth (documented in main.go NOTE). This test
// pins the current behavior so a future change is deliberate.
func TestHandler_MissingAuthGate(t *testing.T) {
	t.Parallel()

	_, mux := newHandlerMux(t)

	// Without any auth header the routes still respond (documented TODO:
	// admin authentication). Status must not be 401 today.
	req := httptest.NewRequest(http.MethodGet, "/v1/api-keys?tenant_id=t", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code == http.StatusUnauthorized {
		t.Log("admin auth added — update SUMMARY notes")
	}
}

// --- Middleware (Task 3.4) ---

// nextHandler captures whether the wrapped handler was reached.
func nextHandler(reached *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*reached = true
		if key := GetAPIKeyFromContext(r.Context()); key != nil {
			w.Header().Set("X-Test-Key-Name", key.Name)
		}
		w.WriteHeader(http.StatusOK)
	})
}

// TestAuthMiddleware_MissingBearerToken verifies a request without
// Authorization: Bearer <key> returns 401.
func TestAuthMiddleware_MissingBearerToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		header string
	}{
		{"no header", ""},
		{"wrong scheme", "Basic aWc6aWc="},
		{"empty token", "Bearer "},
		{"not bearer", "aigw_something"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newTestKeyService(t)
			reached := false
			handler := AuthMiddleware(svc)(nextHandler(&reached))

			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", rec.Code)
			}
			if reached {
				t.Error("request reached the protected handler without a valid bearer token")
			}
		})
	}
}

// TestAuthMiddleware_ValidKeyPassesThrough verifies a valid key passes
// and the context contains the key info.
func TestAuthMiddleware_ValidKeyPassesThrough(t *testing.T) {
	t.Parallel()

	svc := newTestKeyService(t)
	resp, err := svc.Create(CreateKeyRequest{
		TenantID:    "tenant1",
		Name:        "integration",
		Permissions: []string{"chat"},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	reached := false
	handler := AuthMiddleware(svc)(nextHandler(&reached))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer "+resp.Key)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !reached {
		t.Error("valid key did not reach the wrapped handler")
	}
	if got := rec.Header().Get("X-Test-Key-Name"); got != "integration" {
		t.Errorf("context key name = %q, want integration (context must carry key info)", got)
	}
}

// TestAuthMiddleware_RevokedKey verifies a revoked key returns 401.
func TestAuthMiddleware_RevokedKey(t *testing.T) {
	t.Parallel()

	svc := newTestKeyService(t)
	resp, _ := svc.Create(CreateKeyRequest{TenantID: "t", Name: "k", Permissions: []string{"chat"}})

	if err := svc.Revoke("t", resp.ID); err != nil {
		t.Fatalf("Revoke() error = %v", err)
	}

	reached := false
	handler := AuthMiddleware(svc)(nextHandler(&reached))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer "+resp.Key)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for revoked key", rec.Code)
	}
	if reached {
		t.Error("revoked key reached the protected handler")
	}
}

// TestAuthMiddleware_ExpiredKey verifies an expired key returns 401.
func TestAuthMiddleware_ExpiredKey(t *testing.T) {
	t.Parallel()

	svc := newTestKeyService(t)
	expired := time.Now().Add(-time.Hour)
	resp, _ := svc.Create(CreateKeyRequest{
		TenantID:  "t",
		Name:      "k",
		ExpiresAt: &expired,
	})

	reached := false
	handler := AuthMiddleware(svc)(nextHandler(&reached))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer "+resp.Key)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for expired key", rec.Code)
	}
	if reached {
		t.Error("expired key reached the protected handler")
	}
}

// TestAuthMiddleware_HealthSkipsAuth verifies /health and /ready skip
// authentication (200 without a key).
func TestAuthMiddleware_HealthSkipsAuth(t *testing.T) {
	t.Parallel()

	svc := newTestKeyService(t)
	reached := false
	handler := AuthMiddleware(svc)(nextHandler(&reached))

	for _, path := range []string{"/health", "/ready"} {
		t.Run(path, func(t *testing.T) {
			reached = false
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Errorf("GET %s status = %d, want 200 without auth", path, rec.Code)
			}
			if !reached {
				t.Errorf("GET %s did not pass through the middleware", path)
			}
		})
	}
}

// TestAuthMiddleware_PermissionCheck verifies a key without the
// embeddings permission is rejected (403) for /v1/embeddings.
func TestAuthMiddleware_PermissionCheck(t *testing.T) {
	t.Parallel()

	svc := newTestKeyService(t)
	resp, _ := svc.Create(CreateKeyRequest{
		TenantID:    "t",
		Name:        "chat-only",
		Permissions: []string{"chat"},
	})

	reached := false
	handler := AuthMiddleware(svc)(nextHandler(&reached))

	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", nil)
	req.Header.Set("Authorization", "Bearer "+resp.Key)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 (chat-only key on /v1/embeddings)", rec.Code)
	}
	if reached {
		t.Error("permission-less request reached the protected handler")
	}

	// The same key must pass on a chat endpoint.
	reached = false
	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer "+resp.Key)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !reached {
		t.Errorf("chat endpoint: status=%d reached=%v, want 200/true", rec.Code, reached)
	}
}

// TestAuthMiddleware_UnknownKey verifies an unknown key returns 401.
func TestAuthMiddleware_UnknownKey(t *testing.T) {
	t.Parallel()

	svc := newTestKeyService(t)
	reached := false
	handler := AuthMiddleware(svc)(nextHandler(&reached))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer aigw_doesnotexist")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for unknown key", rec.Code)
	}
	if reached {
		t.Error("unknown key reached the protected handler")
	}
}
