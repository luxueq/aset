package gateway

import (
	"math"
	"time"
)

// RetryConfig holds retry behavior settings.
type RetryConfig struct {
	MaxRetries     int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	RetryOn        []int
}

// DefaultRetryConfig returns sensible defaults.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxRetries:     3,
		InitialBackoff: 500 * time.Millisecond,
		MaxBackoff:     30 * time.Second,
		RetryOn:        []int{429, 500, 502, 503},
	}
}

// shouldRetry checks if a status code is retryable.
func (c RetryConfig) shouldRetry(statusCode int) bool {
	for _, code := range c.RetryOn {
		if statusCode == code {
			return true
		}
	}
	return false
}

// backoff calculates exponential backoff.
func (c RetryConfig) backoff(attempt int) time.Duration {
	backoff := float64(c.InitialBackoff) * math.Pow(2, float64(attempt))
	if backoff > float64(c.MaxBackoff) {
		return c.MaxBackoff
	}
	return time.Duration(backoff)
}
