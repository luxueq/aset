package auth

import (
	"encoding/json"
	"net/http"
	"strings"
)

// Handler provides HTTP handlers for API key management endpoints.
type Handler struct {
	keyService *KeyService
}

// NewHandler creates a new Handler.
func NewHandler(keyService *KeyService) *Handler {
	return &Handler{keyService: keyService}
}

// RegisterRoutes registers API key management routes on the given mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/api-keys", h.createKey)
	mux.HandleFunc("GET /v1/api-keys", h.listKeys)
	mux.HandleFunc("GET /v1/api-keys/", h.getKey)
	mux.HandleFunc("DELETE /v1/api-keys/", h.deleteKey)
}

// createKey handles POST /v1/api-keys
func (h *Handler) createKey(w http.ResponseWriter, r *http.Request) {
	var req CreateKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_body")
		return
	}

	// Validate required fields
	if req.TenantID == "" {
		writeError(w, http.StatusBadRequest, "tenant_id_is_required")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name_is_required")
		return
	}
	if len(req.Permissions) == 0 {
		req.Permissions = []string{"chat"} // Default permission
	}

	resp, err := h.keyService.Create(req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed_to_create_api_key")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(resp)
}

// listKeys handles GET /v1/api-keys?tenant_id=xxx
func (h *Handler) listKeys(w http.ResponseWriter, r *http.Request) {
	tenantID := r.URL.Query().Get("tenant_id")
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "tenant_id_query_param_is_required")
		return
	}

	keys, err := h.keyService.List(tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed_to_list_api_keys")
		return
	}

	// Return list response (without sensitive fields)
	type keyResponse struct {
		ID          string   `json:"id"`
		KeyPrefix   string   `json:"key_prefix"`
		TenantID    string   `json:"tenant_id"`
		Name        string   `json:"name"`
		Permissions []string `json:"permissions"`
		Revoked     bool     `json:"revoked"`
		CreatedAt   string   `json:"created_at"`
		LastUsedAt  *string  `json:"last_used_at,omitempty"`
	}

	var resp []keyResponse
	for _, k := range keys {
		item := keyResponse{
			ID:          k.ID,
			KeyPrefix:   k.KeyPrefix,
			TenantID:    k.TenantID,
			Name:        k.Name,
			Permissions: k.Permissions,
			Revoked:     k.Revoked,
			CreatedAt:   k.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		}
		if k.LastUsedAt != nil {
			s := k.LastUsedAt.Format("2006-01-02T15:04:05Z07:00")
			item.LastUsedAt = &s
		}
		resp = append(resp, item)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"data": resp,
	})
}

// getKey handles GET /v1/api-keys/:id
func (h *Handler) getKey(w http.ResponseWriter, r *http.Request) {
	// Extract key ID from URL path: /v1/api-keys/{id}
	id := strings.TrimPrefix(r.URL.Path, "/v1/api-keys/")
	if id == "" {
		writeError(w, http.StatusBadRequest, "api_key_id_is_required")
		return
	}

	// Get tenant ID from query param (in production, extract from auth context)
	tenantID := r.URL.Query().Get("tenant_id")
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "tenant_id_query_param_is_required")
		return
	}

	apiKey, err := h.keyService.Get(tenantID, id)
	if err != nil {
		if err == ErrKeyNotFound {
			writeError(w, http.StatusNotFound, "api_key_not_found")
		} else {
			writeError(w, http.StatusInternalServerError, "failed_to_get_api_key")
		}
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(apiKey)
}

// deleteKey handles DELETE /v1/api-keys/:id
// The key is revoked (soft delete), not removed: revoked keys fail
// validation with 401 on the next request. Full removal is an admin/DB
// operation.
func (h *Handler) deleteKey(w http.ResponseWriter, r *http.Request) {
	// Extract key ID from URL path: /v1/api-keys/{id}
	id := strings.TrimPrefix(r.URL.Path, "/v1/api-keys/")
	if id == "" {
		writeError(w, http.StatusBadRequest, "api_key_id_is_required")
		return
	}

	// Get tenant ID from query param (in production, extract from auth context)
	tenantID := r.URL.Query().Get("tenant_id")
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "tenant_id_query_param_is_required")
		return
	}

	if err := h.keyService.Revoke(tenantID, id); err != nil {
		if err == ErrKeyNotFound {
			writeError(w, http.StatusNotFound, "api_key_not_found")
		} else {
			writeError(w, http.StatusInternalServerError, "failed_to_revoke_api_key")
		}
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"status": "revoked",
		"id":     id,
	})
}
