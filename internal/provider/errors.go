package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// APIError represents a unified error response in OpenAI-compatible format.
type APIError struct {
	StatusCode int    `json:"-"`
	Type       string `json:"type"`
	Message    string `json:"message"`
	Code       string `json:"code,omitempty"`
	Provider   string `json:"-"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("[%s] %s: %s", e.Provider, e.Type, e.Message)
}

// ToJSON returns the error in OpenAI-compatible format.
func (e *APIError) ToJSON() []byte {
	resp := map[string]interface{}{
		"error": map[string]interface{}{
			"message": e.Message,
			"type":    e.Type,
		},
	}
	if e.Code != "" {
		resp["error"].(map[string]interface{})["code"] = e.Code
	}
	data, _ := json.Marshal(resp)
	return data
}

// ParseProviderError parses a provider error response into unified APIError.
func ParseProviderError(resp *http.Response, providerName string) *APIError {
	if resp == nil {
		return &APIError{
			StatusCode: http.StatusBadGateway,
			Type:       "provider_error",
			Message:    "nil response from provider",
			Provider:   providerName,
		}
	}

	var body map[string]interface{}
	if resp.Body != nil {
		json.NewDecoder(resp.Body).Decode(&body)
	}

	apiErr := &APIError{
		StatusCode: resp.StatusCode,
		Provider:   providerName,
	}

	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		apiErr.Type = "authentication_error"
		apiErr.Message = extractErrorMessage(body, "invalid or missing API key")
	case http.StatusTooManyRequests:
		apiErr.Type = "rate_limit_error"
		apiErr.Message = extractErrorMessage(body, "rate limit exceeded")
	case http.StatusInternalServerError:
		apiErr.Type = "server_error"
		apiErr.Message = extractErrorMessage(body, "provider internal error")
	case http.StatusServiceUnavailable:
		apiErr.Type = "service_unavailable"
		apiErr.Message = extractErrorMessage(body, "provider temporarily unavailable")
	case http.StatusBadRequest:
		apiErr.Type = "invalid_request_error"
		apiErr.Message = extractErrorMessage(body, "invalid request")
	default:
		apiErr.Type = "provider_error"
		apiErr.Message = extractErrorMessage(body, fmt.Sprintf("provider returned status %d", resp.StatusCode))
	}

	if body != nil {
		if errObj, ok := body["error"].(map[string]interface{}); ok {
			if code, ok := errObj["code"].(string); ok {
				apiErr.Code = code
			}
		}
	}

	return apiErr
}

func extractErrorMessage(body map[string]interface{}, fallback string) string {
	if body == nil {
		return fallback
	}
	// OpenAI/Anthropic: {"error": {"message": "..."}}
	if errObj, ok := body["error"].(map[string]interface{}); ok {
		if msg, ok := errObj["message"].(string); ok {
			return msg
		}
	}
	// Plain string: {"error": "..."}
	if errMsg, ok := body["error"].(string); ok {
		return errMsg
	}
	// Top level: {"message": "..."}
	if msg, ok := body["message"].(string); ok {
		return msg
	}
	return fallback
}
