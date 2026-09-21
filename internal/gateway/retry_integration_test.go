package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/luxueq/aigateway/internal/common/config"
	"github.com/luxueq/aigateway/internal/common/logger"
)

// TestDoWithRetry_NonRetryableStatusReturnedImmediately verifies a 400
// response is returned on the first attempt without any backoff wait.
func TestDoWithRetry_NonRetryableStatusReturnedImmediately(t *testing.T) {
	t.Parallel()

	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	log, _ := logger.Init(config.LoggingConfig{Level: "error", Format: "json"})
	p := &Proxy{httpClient: srv.Client(), logger: log, retryConfig: fastRetryConfig()}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}

	start := time.Now()
	resp, err := p.doWithRetry(context.Background(), req)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	if calls != 1 {
		t.Errorf("upstream calls = %d, want 1 (400 must not be retried)", calls)
	}
	if elapsed > 100*time.Millisecond {
		t.Errorf("elapsed = %v, want < 100ms (no backoff wait on non-retryable)", elapsed)
	}
}

// TestDoWithRetry_RetriesOnRetryableStatus verifies a 429 response is
// retried and eventually succeeds.
func TestDoWithRetry_RetriesOnRetryableStatus(t *testing.T) {
	t.Parallel()

	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	log, _ := logger.Init(config.LoggingConfig{Level: "error", Format: "json"})
	p := &Proxy{httpClient: srv.Client(), logger: log, retryConfig: fastRetryConfig()}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, strings.NewReader("{}"))

	resp, err := p.doWithRetry(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 after retries", resp.StatusCode)
	}
	if calls != 3 {
		t.Errorf("upstream calls = %d, want 3 (two 429s then success)", calls)
	}
}

// TestDoWithRetry_ExhaustsRetries verifies MaxRetries bounds the attempts
// and the last retryable response is returned.
func TestDoWithRetry_ExhaustsRetries(t *testing.T) {
	t.Parallel()

	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	log, _ := logger.Init(config.LoggingConfig{Level: "error", Format: "json"})
	rc := fastRetryConfig()
	p := &Proxy{httpClient: srv.Client(), logger: log, retryConfig: rc}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, strings.NewReader("{}"))

	resp, err := p.doWithRetry(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 (last retryable response)", resp.StatusCode)
	}
	if want := rc.MaxRetries + 1; calls != want {
		t.Errorf("upstream calls = %d, want %d (1 + MaxRetries)", calls, want)
	}
}

// TestDoWithRetry_ContextCancellationStopsLoop verifies a cancelled context
// aborts the retry loop immediately during the backoff wait.
func TestDoWithRetry_ContextCancellationStopsLoop(t *testing.T) {
	t.Parallel()

	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	log, _ := logger.Init(config.LoggingConfig{Level: "error", Format: "json"})
	// Long backoff so cancellation happens while waiting, not between quick calls.
	p := &Proxy{httpClient: srv.Client(), logger: log, retryConfig: RetryConfig{
		MaxRetries:     5,
		InitialBackoff: 5 * time.Second,
		MaxBackoff:     30 * time.Second,
		RetryOn:        []int{500},
	}}

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL, strings.NewReader("{}"))

	// Cancel shortly after the first attempt (during the 5s backoff wait).
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := p.doWithRetry(ctx, req)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected context cancellation error")
	}
	if !strings.Contains(err.Error(), "context canceled") {
		t.Errorf("err = %v, want context canceled", err)
	}
	if elapsed > time.Second {
		t.Errorf("elapsed = %v, want < 1s (cancellation must stop the loop immediately)", elapsed)
	}
	if calls != 1 {
		t.Errorf("upstream calls = %d, want 1 (no retry after cancel)", calls)
	}
}

// fastRetryConfig keeps retry tests fast while preserving semantics.
func fastRetryConfig() RetryConfig {
	return RetryConfig{
		MaxRetries:     3,
		InitialBackoff: time.Millisecond,
		MaxBackoff:     5 * time.Millisecond,
		RetryOn:        []int{429, 500, 502, 503},
	}
}
