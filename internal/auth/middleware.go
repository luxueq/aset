package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/luxueq/aigateway/internal/provider"
)

// contextKey is a private type for context keys to avoid collisions.
type contextKey string

const (
	// apiKeyContextKey is the context key for storing the validated API key.
	apiKeyContextKey contextKey = "api_key"
)

// AuthMiddleware creates an HTTP middleware that validates API keys.
// It extracts the bearer token from the Authorization header, validates it,
// and stores the API key metadata in the request context.
func AuthMiddleware(keyService *KeyService) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip authentication for health check endpoints
			if r.URL.Path == "/health" || r.URL.Path == "/ready" {
				next.ServeHTTP(w, r)
				return
			}

			// Extract bearer token from Authorization header
			authHeader := r.Header.Get("Authorization")
			plaintextKey, err := ExtractKeyFromHeader(authHeader)
			if err != nil {
				writeError(w, http.StatusUnauthorized, "missing_or_invalid_authorization_header")
				return
			}

			// Validate the API key
			apiKey, err := keyService.Validate(plaintextKey)
			if err != nil {
				switch err {
				case ErrKeyNotFound:
					writeError(w, http.StatusUnauthorized, "invalid_api_key")
				case ErrKeyRevoked:
					writeError(w, http.StatusUnauthorized, "api_key_has_been_revoked")
				case ErrKeyExpired:
					writeError(w, http.StatusUnauthorized, "api_key_has_expired")
				default:
					writeError(w, http.StatusInternalServerError, "authentication_error")
				}
				return
			}

			// Check permission based on endpoint
			requiredPermission := getRequiredPermission(r.URL.Path)
			if requiredPermission != "" && !apiKey.HasPermission(requiredPermission) {
				writeError(w, http.StatusForbidden, "insufficient_permissions")
				return
			}

			// Store API key in context for downstream handlers
			ctx := context.WithValue(r.Context(), apiKeyContextKey, apiKey)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// GetAPIKeyFromContext retrieves the validated API key from the request context.
// Returns nil if no API key is present (should not happen if AuthMiddleware is used).
func GetAPIKeyFromContext(ctx context.Context) *APIKey {
	apiKey, ok := ctx.Value(apiKeyContextKey).(*APIKey)
	if !ok {
		return nil
	}
	return apiKey
}

// getRequiredPermission returns the required permission for a given path.
func getRequiredPermission(path string) string {
	// Map paths to required permissions
	switch {
	case strings.Contains(path, "/chat/completions"):
		return "chat"
	case strings.Contains(path, "/embeddings"):
		return "embeddings"
	case strings.Contains(path, "/images"):
		return "images"
	default:
		return "" // No specific permission required
	}
}

// writeError writes a JSON error response in OpenAI format.
func writeError(w http.ResponseWriter, statusCode int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	apiErr := &provider.APIError{
		StatusCode: statusCode,
		Type:       "authentication_error",
		Message:    message,
		Code:       message,
	}

	w.Write(apiErr.ToJSON())
}
