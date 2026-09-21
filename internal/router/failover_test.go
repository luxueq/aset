package router

import (
	"errors"
	"testing"
)

var errUpstream = errors.New("upstream unavailable")

// TestRouteWithFailover_PrimaryFailsOverToNext verifies the primary
// provider failing triggers an automatic retry on the next provider.
func TestRouteWithFailover_PrimaryFailsOverToNext(t *testing.T) {
	t.Parallel()

	r := New(Config{
		Routes: []RouteRule{
			{Model: "gpt-4", Providers: []string{"openai", "anthropic", "google"}, Strategy: "cost"},
		},
	})
	r.SetProviders([]*ProviderStatus{
		{Name: "openai", Healthy: true, CostPer1K: 0.01}, // primary (cheapest)
		{Name: "anthropic", Healthy: true, CostPer1K: 0.02},
		{Name: "google", Healthy: true, CostPer1K: 0.03},
	})

	var tried []string
	selected, attempts, err := r.RouteWithFailover("gpt-4", DefaultFailoverConfig(), func(c *ProviderStatus) error {
		tried = append(tried, c.Name)
		if c.Name == "openai" {
			return errUpstream
		}
		return nil
	})

	if err != nil {
		t.Fatalf("RouteWithFailover() error = %v, want failover success", err)
	}
	if selected.Name != "anthropic" {
		t.Errorf("selected = %q, want anthropic (second candidate after openai failure)", selected.Name)
	}
	if len(tried) != 2 || tried[0] != "openai" || tried[1] != "anthropic" {
		t.Errorf("tried = %v, want [openai anthropic]", tried)
	}
	if len(attempts) != 1 || attempts[0].Provider != "openai" {
		t.Errorf("attempts = %v, want one failed openai attempt", attempts)
	}

	// Passive tracking: the failure was recorded for openai.
	if st, _ := r.Status("openai"); st.ErrorCount != 1 {
		t.Errorf("openai ErrorCount = %d, want 1 (failure recorded)", st.ErrorCount)
	}
}

// TestRouteWithFailover_MaxAttempts verifies the default cap of 3
// providers: all fail -> ErrAllProvidersFailed, exactly 3 attempts.
func TestRouteWithFailover_MaxAttempts(t *testing.T) {
	t.Parallel()

	r := New(Config{
		Routes: []RouteRule{
			{Model: "gpt-4", Providers: []string{"openai", "anthropic", "google", "extra"}},
		},
	})
	r.SetProviders([]*ProviderStatus{
		{Name: "openai", Healthy: true},
		{Name: "anthropic", Healthy: true},
		{Name: "google", Healthy: true},
		{Name: "extra", Healthy: true},
	})

	var tried []string
	_, attempts, err := r.RouteWithFailover("gpt-4", DefaultFailoverConfig(), func(c *ProviderStatus) error {
		tried = append(tried, c.Name)
		return errUpstream
	})

	if !errors.Is(err, ErrAllProvidersFailed) {
		t.Fatalf("error = %v, want ErrAllProvidersFailed", err)
	}
	if len(tried) != 3 {
		t.Errorf("tried %d providers, want 3 (MaxProviders default)", len(tried))
	}
	if len(attempts) != 3 {
		t.Errorf("attempts = %d, want 3", len(attempts))
	}
	for _, a := range attempts {
		if a.Err == nil {
			t.Errorf("attempt for %q has nil Err, want errUpstream", a.Provider)
		}
	}
}

// TestRouteWithFailover_CustomMaxProviders verifies a non-default cap.
func TestRouteWithFailover_CustomMaxProviders(t *testing.T) {
	t.Parallel()

	r := New(Config{
		Routes: []RouteRule{{Model: "m", Providers: []string{"a", "b", "c"}}},
	})
	r.SetProviders([]*ProviderStatus{
		{Name: "a", Healthy: true},
		{Name: "b", Healthy: true},
		{Name: "c", Healthy: true},
	})

	cfg := FailoverConfig{Enabled: true, MaxProviders: 2}
	var tried []string
	_, _, err := r.RouteWithFailover("m", cfg, func(c *ProviderStatus) error {
		tried = append(tried, c.Name)
		return errUpstream
	})

	if !errors.Is(err, ErrAllProvidersFailed) {
		t.Fatalf("error = %v, want ErrAllProvidersFailed", err)
	}
	if len(tried) != 2 {
		t.Errorf("tried %d providers, want 2 (custom MaxProviders)", len(tried))
	}
}

// TestRouteWithFailover_ReturnOnError verifies ReturnOnError=true returns
// the provider error to the client instead of failing over.
func TestRouteWithFailover_ReturnOnError(t *testing.T) {
	t.Parallel()

	r := New(Config{
		Routes: []RouteRule{{Model: "gpt-4", Providers: []string{"openai", "anthropic"}}},
	})
	r.SetProviders([]*ProviderStatus{
		{Name: "openai", Healthy: true},
		{Name: "anthropic", Healthy: true},
	})

	cfg := FailoverConfig{Enabled: true, ReturnOnError: true}
	var tried []string
	_, _, err := r.RouteWithFailover("gpt-4", cfg, func(c *ProviderStatus) error {
		tried = append(tried, c.Name)
		if c.Name == "openai" {
			return errUpstream
		}
		return nil
	})

	if !errors.Is(err, errUpstream) {
		t.Fatalf("error = %v, want errUpstream returned to client", err)
	}
	if len(tried) != 1 || tried[0] != "openai" {
		t.Errorf("tried = %v, want only [openai] (no failover when ReturnOnError)", tried)
	}
}

// TestRouteWithFailover_Disabled verifies Enabled=false also returns the
// first provider's result without failover.
func TestRouteWithFailover_Disabled(t *testing.T) {
	t.Parallel()

	r := New(Config{
		Routes: []RouteRule{{Model: "gpt-4", Providers: []string{"openai", "anthropic"}}},
	})
	r.SetProviders([]*ProviderStatus{
		{Name: "openai", Healthy: true},
		{Name: "anthropic", Healthy: true},
	})

	cfg := FailoverConfig{Enabled: false}
	var tried []string
	_, _, err := r.RouteWithFailover("gpt-4", cfg, func(c *ProviderStatus) error {
		tried = append(tried, c.Name)
		return nil
	})

	if err != nil {
		t.Fatalf("error = %v, want success", err)
	}
	if len(tried) != 1 {
		t.Errorf("tried = %v, want single provider when failover disabled", tried)
	}
}

// TestRouteWithFailover_NoCandidates verifies an empty candidate list
// surfaces ErrNoHealthyProvider.
func TestRouteWithFailover_NoCandidates(t *testing.T) {
	t.Parallel()

	r := New(Config{})
	r.SetProviders([]*ProviderStatus{{Name: "openai", Healthy: false}})

	_, _, err := r.RouteWithFailover("gpt-4", DefaultFailoverConfig(), func(c *ProviderStatus) error {
		return nil
	})
	if !errors.Is(err, ErrNoHealthyProvider) {
		t.Fatalf("error = %v, want ErrNoHealthyProvider", err)
	}
}

// TestRouteWithFailover_FirstSucceeds verifies the happy path: one
// attempt, success recorded, no failover attempts.
func TestRouteWithFailover_FirstSucceeds(t *testing.T) {
	t.Parallel()

	r := New(Config{
		Routes: []RouteRule{{Model: "gpt-4", Providers: []string{"openai", "anthropic"}}},
	})
	r.SetProviders([]*ProviderStatus{
		{Name: "openai", Healthy: true},
		{Name: "anthropic", Healthy: true},
	})

	var tried []string
	selected, attempts, err := r.RouteWithFailover("gpt-4", DefaultFailoverConfig(), func(c *ProviderStatus) error {
		tried = append(tried, c.Name)
		return nil
	})

	if err != nil {
		t.Fatalf("error = %v, want success", err)
	}
	if selected == nil || selected.Name != "openai" {
		t.Errorf("selected = %v, want openai", selected)
	}
	if len(tried) != 1 || len(attempts) != 0 {
		t.Errorf("tried=%v attempts=%v, want single successful attempt", tried, attempts)
	}
}
