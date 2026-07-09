package gateway

import (
	"testing"
	"time"
)

func TestRetryConfig_ShouldRetry(t *testing.T) {
	cfg := DefaultRetryConfig()

	tests := []struct {
		statusCode int
		want       bool
	}{
		{200, false},
		{400, false},
		{401, false},
		{403, false},
		{404, false},
		{429, true},
		{500, true},
		{502, true},
		{503, true},
		{504, false},
	}

	for _, tt := range tests {
		got := cfg.shouldRetry(tt.statusCode)
		if got != tt.want {
			t.Errorf("shouldRetry(%d) = %v, want %v", tt.statusCode, got, tt.want)
		}
	}
}

func TestRetryConfig_Backoff(t *testing.T) {
	cfg := DefaultRetryConfig()

	// Attempt 0: 500ms
	b0 := cfg.backoff(0)
	if b0 != 500*time.Millisecond {
		t.Errorf("backoff(0) = %v, want 500ms", b0)
	}

	// Attempt 1: 1s
	b1 := cfg.backoff(1)
	if b1 != 1*time.Second {
		t.Errorf("backoff(1) = %v, want 1s", b1)
	}

	// Attempt 2: 2s
	b2 := cfg.backoff(2)
	if b2 != 2*time.Second {
		t.Errorf("backoff(2) = %v, want 2s", b2)
	}

	// Attempt 10: should cap at MaxBackoff (30s)
	b10 := cfg.backoff(10)
	if b10 != 30*time.Second {
		t.Errorf("backoff(10) = %v, want 30s (capped)", b10)
	}
}
