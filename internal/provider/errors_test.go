package provider

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestParseProviderError_OpenAIFormat(t *testing.T) {
	body := `{"error": {"message": "Invalid API key", "type": "invalid_request_error", "code": "invalid_api_key"}}`
	resp := &http.Response{
		StatusCode: 401,
		Body:       io.NopCloser(strings.NewReader(body)),
	}

	err := ParseProviderError(resp, "openai")

	if err.StatusCode != 401 {
		t.Errorf("StatusCode = %d, want 401", err.StatusCode)
	}
	if err.Type != "authentication_error" {
		t.Errorf("Type = %q, want %q", err.Type, "authentication_error")
	}
	if err.Message != "Invalid API key" {
		t.Errorf("Message = %q, want %q", err.Message, "Invalid API key")
	}
	if err.Provider != "openai" {
		t.Errorf("Provider = %q, want %q", err.Provider, "openai")
	}
}

func TestParseProviderError_NilResponse(t *testing.T) {
	err := ParseProviderError(nil, "openai")

	if err.StatusCode != http.StatusBadGateway {
		t.Errorf("StatusCode = %d, want %d", err.StatusCode, http.StatusBadGateway)
	}
}

func TestAPIError_ToJSON(t *testing.T) {
	err := &APIError{
		Type:    "rate_limit_error",
		Message: "You exceeded your current quota",
		Code:    "rate_limit_exceeded",
	}

	jsonBytes := err.ToJSON()
	expected := `{"error":{"code":"rate_limit_exceeded","message":"You exceeded your current quota","type":"rate_limit_error"}}`
	if string(jsonBytes) != expected {
		t.Errorf("ToJSON() = %s, want %s", string(jsonBytes), expected)
	}
}

func TestFilterUnsupportedParams_Drop(t *testing.T) {
	params := map[string]interface{}{
		"model":       "gpt-4",
		"messages":    []interface{}{},
		"temperature": 0.7,
		"logit_bias":  map[string]float64{},
		"top_k":       40,
	}

	filtered, dropped, err := FilterUnsupportedParams(params, "openai", ParamDrop)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(dropped) != 1 || dropped[0] != "top_k" {
		t.Errorf("dropped = %v, want [top_k]", dropped)
	}
	if _, ok := filtered["top_k"]; ok {
		t.Error("top_k should be filtered out")
	}
	if _, ok := filtered["model"]; !ok {
		t.Error("model should be kept")
	}
}

func TestFilterUnsupportedParams_Strict(t *testing.T) {
	params := map[string]interface{}{
		"model": "claude-3",
		"top_k": 40,
		"n":     2,
	}

	_, dropped, err := FilterUnsupportedParams(params, "anthropic", ParamStrict)

	if err == nil {
		t.Fatal("expected error in strict mode")
	}
	if len(dropped) == 0 {
		t.Error("expected dropped params")
	}
}

func TestFilterUnsupportedParams_UnknownProvider(t *testing.T) {
	params := map[string]interface{}{
		"model":  "test",
		"custom": "value",
	}

	filtered, dropped, err := FilterUnsupportedParams(params, "unknown", ParamDrop)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(dropped) != 0 {
		t.Error("unknown provider should not drop any params")
	}
	if len(filtered) != len(params) {
		t.Error("unknown provider should pass all params through")
	}
}
