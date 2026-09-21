package router

import (
	"testing"
	"time"

	"github.com/luxueq/aigateway/internal/provider"
)

func newTestRouter() *Router {
	r := New(Config{DefaultStrategy: "roundrobin"})
	r.SetProviders([]*ProviderStatus{
		{Name: "openai", Healthy: true},
		{Name: "anthropic", Healthy: true},
		{Name: "google", Healthy: true},
	})
	return r
}

// TestRouter_StrategiesRegistered verifies all four strategies exist and
// the Strategy interface contract is satisfied.
func TestRouter_StrategiesRegistered(t *testing.T) {
	t.Parallel()

	r := newTestRouter()

	for _, name := range []string{"roundrobin", "latency", "cost", "weighted"} {
		s, ok := r.strategies[name]
		if !ok {
			t.Errorf("strategy %q not registered", name)
			continue
		}
		if s.Name() != name {
			t.Errorf("strategy Name() = %q, want %q", s.Name(), name)
		}
	}
}

// TestRouter_RoundRobinCyclesEvenly verifies the 100-request round-robin
// criterion: selections distribute exactly evenly across providers.
func TestRouter_RoundRobinCyclesEvenly(t *testing.T) {
	t.Parallel()

	r := newTestRouter()

	counts := make(map[string]int)
	for i := 0; i < 100; i++ {
		p, err := r.Select("roundrobin", &provider.CanonicalRequest{Model: "gpt-4"})
		if err != nil {
			t.Fatalf("Select() error = %v", err)
		}
		counts[p.Name]++
	}

	// 100 requests over 3 providers: 34/33/33
	want := map[string]int{"openai": 34, "anthropic": 33, "google": 33}
	for name, n := range want {
		if counts[name] != n {
			t.Errorf("roundrobin selected %q %d times in 100 requests, want %d", name, counts[name], n)
		}
	}
}

// TestRouter_CandidatesRouteRule verifies a route rule resolves the correct
// provider pool and puts the strategy's primary choice first.
func TestRouter_CandidatesRouteRule(t *testing.T) {
	t.Parallel()

	r := New(Config{
		DefaultStrategy: "roundrobin",
		Routes: []RouteRule{
			{Model: "gpt-4", Providers: []string{"openai", "anthropic"}, Strategy: "cost"},
		},
	})
	r.SetProviders([]*ProviderStatus{
		{Name: "openai", Healthy: true, CostPer1K: 0.03},
		{Name: "anthropic", Healthy: true, CostPer1K: 0.015},
		{Name: "google", Healthy: true, CostPer1K: 0.001},
	})

	candidates := r.Candidates("gpt-4")
	if len(candidates) != 2 {
		t.Fatalf("Candidates(gpt-4) returned %d providers, want 2 (route rule pool)", len(candidates))
	}
	// Cost strategy: anthropic (0.015) must be the primary choice.
	if candidates[0].Name != "anthropic" {
		t.Errorf("primary candidate = %q, want anthropic (cheapest in pool)", candidates[0].Name)
	}

	// Full model string must match the same rule.
	candidates = r.Candidates("openai/gpt-4")
	if len(candidates) != 2 {
		t.Errorf("Candidates(openai/gpt-4) returned %d providers, want 2 (bare-model rule match)", len(candidates))
	}

	// Unrouted model: all healthy providers.
	candidates = r.Candidates("claude-3-opus")
	if len(candidates) != 3 {
		t.Errorf("Candidates(claude-3-opus) returned %d providers, want 3 (default pool)", len(candidates))
	}
}

// TestRouter_CandidatesProviderPinned verifies an explicit "provider/model"
// request (no matching route rule) pins that single provider.
func TestRouter_CandidatesProviderPinned(t *testing.T) {
	t.Parallel()

	r := newTestRouter()

	candidates := r.Candidates("anthropic/claude-3-opus")
	if len(candidates) != 1 {
		t.Fatalf("Candidates(anthropic/claude-3-opus) returned %d providers, want 1 (pinned)", len(candidates))
	}
	if candidates[0].Name != "anthropic" {
		t.Errorf("pinned candidate = %q, want anthropic", candidates[0].Name)
	}

	// Pin to a provider that is not registered: no candidates.
	if got := r.Candidates("unknown/model"); got != nil {
		t.Errorf("Candidates(unknown/model) = %v, want nil", got)
	}
}

// TestRouter_CandidatesExcludesUnhealthy verifies unhealthy providers are
// excluded from the candidate list until recovery.
func TestRouter_CandidatesExcludesUnhealthy(t *testing.T) {
	t.Parallel()

	r := newTestRouter()
	r.MarkUnhealthy("openai")

	candidates := r.Candidates("gpt-4")
	for _, p := range candidates {
		if p.Name == "openai" {
			t.Error("unhealthy provider openai must be excluded from candidates")
		}
	}
	if len(candidates) != 2 {
		t.Errorf("candidates after marking openai unhealthy = %d, want 2", len(candidates))
	}

	// Recovery: healthy again, back in the pool.
	r.MarkHealthy("openai")
	candidates = r.Candidates("gpt-4")
	if len(candidates) != 3 {
		t.Errorf("candidates after recovery = %d, want 3", len(candidates))
	}
}

// TestRouter_CandidatesAllUnhealthy verifies nil is returned when every
// provider is unhealthy.
func TestRouter_CandidatesAllUnhealthy(t *testing.T) {
	t.Parallel()

	r := newTestRouter()
	r.MarkUnhealthy("openai")
	r.MarkUnhealthy("anthropic")
	r.MarkUnhealthy("google")

	if got := r.Candidates("gpt-4"); got != nil {
		t.Errorf("Candidates with all unhealthy = %v, want nil", got)
	}
}

// TestRouter_SelectNoHealthy verifies Select returns ErrNoHealthyProvider
// when no provider is available.
func TestRouter_SelectNoHealthy(t *testing.T) {
	t.Parallel()

	r := New(Config{})
	r.SetProviders([]*ProviderStatus{{Name: "openai", Healthy: false}})

	_, err := r.Select("", &provider.CanonicalRequest{Model: "gpt-4"})
	if err != ErrNoHealthyProvider {
		t.Errorf("Select() error = %v, want %v", err, ErrNoHealthyProvider)
	}
}

// TestRouter_LatencyStrategy verifies the latency strategy picks the
// provider with the lowest average latency.
func TestRouter_LatencyStrategy(t *testing.T) {
	t.Parallel()

	r := newTestRouter()
	r.SetProviders([]*ProviderStatus{
		{Name: "openai", Healthy: true, AvgLatency: 200 * time.Millisecond},
		{Name: "anthropic", Healthy: true, AvgLatency: 80 * time.Millisecond},
		{Name: "google", Healthy: true, AvgLatency: 150 * time.Millisecond},
	})

	p, err := r.Select("latency", &provider.CanonicalRequest{Model: "gpt-4"})
	if err != nil {
		t.Fatalf("Select(latency) error = %v", err)
	}
	if p.Name != "anthropic" {
		t.Errorf("latency strategy selected %q, want anthropic (80ms)", p.Name)
	}
}

// TestRouter_CostStrategy verifies the cost strategy picks the cheapest
// provider.
func TestRouter_CostStrategy(t *testing.T) {
	t.Parallel()

	r := newTestRouter()
	r.SetProviders([]*ProviderStatus{
		{Name: "openai", Healthy: true, CostPer1K: 0.03},
		{Name: "anthropic", Healthy: true, CostPer1K: 0.015},
		{Name: "google", Healthy: true, CostPer1K: 0.06},
	})

	p, err := r.Select("cost", &provider.CanonicalRequest{Model: "gpt-4"})
	if err != nil {
		t.Fatalf("Select(cost) error = %v", err)
	}
	if p.Name != "anthropic" {
		t.Errorf("cost strategy selected %q, want anthropic (0.015)", p.Name)
	}
}

// TestRouter_WeightedStrategyDistribution verifies the weighted strategy
// distributes selections roughly in proportion to configured weights.
func TestRouter_WeightedStrategyDistribution(t *testing.T) {
	t.Parallel()

	r := newTestRouter()
	r.SetProviders([]*ProviderStatus{
		{Name: "openai", Healthy: true, Weight: 9},
		{Name: "anthropic", Healthy: true, Weight: 1},
	})

	const n = 1000
	counts := make(map[string]int)
	for i := 0; i < n; i++ {
		p, err := r.Select("weighted", &provider.CanonicalRequest{Model: "gpt-4"})
		if err != nil {
			t.Fatalf("Select(weighted) error = %v", err)
		}
		counts[p.Name]++
	}

	// 90/10 split with 1000 draws: allow generous tolerance for randomness.
	if counts["openai"] < 850 || counts["openai"] > 950 {
		t.Errorf("weighted openai = %d/%d, want ~900 (weight 9/10)", counts["openai"], n)
	}
	if counts["anthropic"] < 50 || counts["anthropic"] > 150 {
		t.Errorf("weighted anthropic = %d/%d, want ~100 (weight 1/10)", counts["anthropic"], n)
	}
}

// TestRouter_WeightedStrategyZeroWeights verifies weights <= 0 fall back
// to weight 1 rather than breaking the selection.
func TestRouter_WeightedStrategyZeroWeights(t *testing.T) {
	t.Parallel()

	r := newTestRouter()
	r.SetProviders([]*ProviderStatus{
		{Name: "openai", Healthy: true, Weight: 0},
		{Name: "anthropic", Healthy: true, Weight: -5},
	})

	for i := 0; i < 20; i++ {
		p, err := r.Select("weighted", &provider.CanonicalRequest{Model: "gpt-4"})
		if err != nil {
			t.Fatalf("Select(weighted) error = %v", err)
		}
		if p == nil {
			t.Fatal("Select(weighted) returned nil with zero weights")
		}
	}
}

// TestRouter_UnknownStrategyFallsBack verifies an unknown strategy name
// falls back to the default strategy.
func TestRouter_UnknownStrategyFallsBack(t *testing.T) {
	t.Parallel()

	r := newTestRouter()

	p, err := r.Select("nonexistent", &provider.CanonicalRequest{Model: "gpt-4"})
	if err != nil {
		t.Fatalf("Select(nonexistent) error = %v", err)
	}
	if p == nil {
		t.Fatal("Select(nonexistent) returned nil, want default-strategy fallback")
	}
}

// TestRouter_RecordSuccessUpdatesLatency verifies success recording updates
// the EMA latency and resets the failure streak.
func TestRouter_RecordSuccessUpdatesLatency(t *testing.T) {
	t.Parallel()

	r := newTestRouter()

	r.RecordFailure("openai") // FailStreak = 1
	r.RecordSuccess("openai", 100*time.Millisecond)

	st, ok := r.Status("openai")
	if !ok {
		t.Fatal("Status(openai) not found")
	}
	if st.FailStreak != 0 {
		t.Errorf("FailStreak after success = %d, want 0", st.FailStreak)
	}
	if st.AvgLatency != 100*time.Millisecond {
		t.Errorf("AvgLatency = %v, want 100ms (first sample)", st.AvgLatency)
	}

	r.RecordSuccess("openai", 200*time.Millisecond)
	st, _ = r.Status("openai")
	// EMA: 100*0.8 + 200*0.2 = 120ms
	if st.AvgLatency != 120*time.Millisecond {
		t.Errorf("AvgLatency after second sample = %v, want 120ms", st.AvgLatency)
	}
}

// Error-rate unhealthy marking is covered in health_test.go (Task 3.2
// sliding-window semantics) — a cumulative rate would mark a provider
// unhealthy on its very first failure, which the window fixes.
