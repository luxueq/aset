package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luxueq/aigateway/internal/common/config"
	"github.com/luxueq/aigateway/internal/common/logger"
	"github.com/luxueq/aigateway/internal/provider"
	"github.com/luxueq/aigateway/internal/router"
)

// newRoutingProxy builds a Proxy whose providers point at the given
// httptest upstreams, with routing config routes + failover defaults.
// Retry backoff is shortened for test speed; per-provider retry semantics
// are covered separately in retry_integration_test.go.
func newRoutingProxy(t *testing.T, openaiURL, anthropicURL string, failover *config.Failover) (*Proxy, *config.Config) {
	t.Helper()

	cfg := &config.Config{
		Providers: &config.ProviderConfigs{
			OpenAI:    &config.ProviderConfig{BaseURL: openaiURL, APIKey: "sk-test"},
			Anthropic: &config.ProviderConfig{BaseURL: anthropicURL, APIKey: "ak-test"},
		},
		Routing: &config.RoutingConfig{
			DefaultStrategy: "roundrobin",
			Routes: []config.RouteRule{
				{Model: "gpt-4", Providers: []string{"openai", "anthropic"}},
			},
			Failover: failover,
		},
	}

	log, _ := logger.Init(config.LoggingConfig{Level: "error", Format: "json"})
	p := NewProxy(cfg, log)
	p.retryConfig = fastRetryConfig()
	return p, cfg
}

// TestProxy_RouteAndForward_FailoverOn5xx verifies the end-to-end
// criterion: primary provider failure triggers automatic retry on the
// next provider and the client receives the secondary's success.
func TestProxy_RouteAndForward_FailoverOn5xx(t *testing.T) {
	t.Parallel()

	var openaiCalls, anthropicCalls int
	openai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		openaiCalls++
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer openai.Close()

	anthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		anthropicCalls++
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer anthropic.Close()

	p, _ := newRoutingProxy(t, openai.URL, anthropic.URL, nil)

	canonical := canonicalFor("gpt-4")
	resp, provider, err := p.routeAndForward(context.Background(), "gpt-4", canonical)
	if err != nil {
		t.Fatalf("routeAndForward() error = %v, want failover to anthropic", err)
	}
	defer resp.Body.Close()

	if provider != "anthropic" {
		t.Errorf("provider = %q, want anthropic (openai returned 502)", provider)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 from anthropic", resp.StatusCode)
	}
	// Per-provider retry (D-07) fires before cross-provider failover:
	// openai sees 1 + MaxRetries attempts, anthropic serves once.
	if openaiCalls != 4 {
		t.Errorf("openai calls = %d, want 4 (1 initial + 3 retries on 502)", openaiCalls)
	}
	if anthropicCalls != 1 {
		t.Errorf("anthropic calls = %d, want 1", anthropicCalls)
	}
}

// TestProxy_RouteAndForward_AllProvidersFail verifies exhaustion returns
// ErrAllProvidersFailed after trying every candidate.
func TestProxy_RouteAndForward_AllProvidersFail(t *testing.T) {
	t.Parallel()

	openai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer openai.Close()

	anthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer anthropic.Close()

	p, _ := newRoutingProxy(t, openai.URL, anthropic.URL, nil)

	_, _, err := p.routeAndForward(context.Background(), "gpt-4", canonicalFor("gpt-4"))
	if err == nil {
		t.Fatal("routeAndForward() error = nil, want ErrAllProvidersFailed")
	}
	if !errors.Is(err, router.ErrAllProvidersFailed) {
		t.Errorf("error = %v, want wrapped ErrAllProvidersFailed", err)
	}
}

// TestProxy_RouteAndForward_ReturnOnError verifies ReturnOnError=true
// surfaces the provider error to the client instead of failing over.
func TestProxy_RouteAndForward_ReturnOnError(t *testing.T) {
	t.Parallel()

	var anthropicCalls int
	openai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer openai.Close()

	anthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		anthropicCalls++
		w.WriteHeader(http.StatusOK)
	}))
	defer anthropic.Close()

	failover := &config.Failover{Enabled: true, ReturnOnError: true, MaxProviders: 3}
	p, _ := newRoutingProxy(t, openai.URL, anthropic.URL, failover)

	resp, _, err := p.routeAndForward(context.Background(), "gpt-4", canonicalFor("gpt-4"))
	if err != nil {
		t.Fatalf("routeAndForward() error = %v, want provider 500 surfaced", err)
	}
	defer resp.Body.Close()

	// The provider's 500 response is passed through to the client.
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 (ReturnOnError passes provider response through)", resp.StatusCode)
	}
	if anthropicCalls != 0 {
		t.Errorf("anthropic calls = %d, want 0 (ReturnOnError must not fail over)", anthropicCalls)
	}
}

// TestProxy_RouteAndForward_ModelPinnedToProvider verifies an explicit
// "provider/model" request routes to that provider only.
func TestProxy_RouteAndForward_ModelPinnedToProvider(t *testing.T) {
	t.Parallel()

	var openaiCalls, anthropicCalls int
	openai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		openaiCalls++
		w.WriteHeader(http.StatusOK)
	}))
	defer openai.Close()

	anthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		anthropicCalls++
		w.WriteHeader(http.StatusOK)
	}))
	defer anthropic.Close()

	p, _ := newRoutingProxy(t, openai.URL, anthropic.URL, nil)

	resp, provider, err := p.routeAndForward(context.Background(), "anthropic/claude-3-opus", canonicalFor("claude-3-opus"))
	if err != nil {
		t.Fatalf("routeAndForward() error = %v", err)
	}
	defer resp.Body.Close()

	if provider != "anthropic" {
		t.Errorf("provider = %q, want anthropic (pinned)", provider)
	}
	if openaiCalls != 0 {
		t.Errorf("openai calls = %d, want 0 (pinned request)", openaiCalls)
	}
}

// TestProxy_RouteAndForward_4xxNotFailover verifies a 400 from the
// provider is returned as the final response (client error, not a
// failover trigger).
func TestProxy_RouteAndForward_4xxNotFailover(t *testing.T) {
	t.Parallel()

	var anthropicCalls int
	openai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"message":"bad request"}}`))
	}))
	defer openai.Close()

	anthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		anthropicCalls++
		w.WriteHeader(http.StatusOK)
	}))
	defer anthropic.Close()

	p, _ := newRoutingProxy(t, openai.URL, anthropic.URL, nil)

	resp, provider, err := p.routeAndForward(context.Background(), "gpt-4", canonicalFor("gpt-4"))
	if err != nil {
		t.Fatalf("routeAndForward() error = %v, want 400 surfaced not failed over", err)
	}
	defer resp.Body.Close()

	if provider != "openai" {
		t.Errorf("provider = %q, want openai (400 is a final result)", provider)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	if anthropicCalls != 0 {
		t.Errorf("anthropic calls = %d, want 0 (400 must not trigger failover)", anthropicCalls)
	}
}

// TestProxy_RouteAndForward_NoHealthyProvider verifies an unhealthy pool
// surfaces ErrNoHealthyProvider.
func TestProxy_RouteAndForward_NoHealthyProvider(t *testing.T) {
	t.Parallel()

	openai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer openai.Close()

	p, _ := newRoutingProxy(t, openai.URL, openai.URL, nil)
	p.Router().MarkUnhealthy("openai")
	p.Router().MarkUnhealthy("anthropic")

	_, _, err := p.routeAndForward(context.Background(), "gpt-4", canonicalFor("gpt-4"))
	if err == nil {
		t.Fatal("routeAndForward() error = nil, want ErrNoHealthyProvider")
	}
}

// TestProxy_RouterSeededFromConfig verifies provider status (weight,
// cost) is seeded from provider config and exposed via Router().
func TestProxy_RouterSeededFromConfig(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Providers: &config.ProviderConfigs{
			OpenAI:    &config.ProviderConfig{BaseURL: "http://o", APIKey: "k", Weight: 7, CostPer1K: 0.03},
			Anthropic: &config.ProviderConfig{BaseURL: "http://a", APIKey: "k", CostPer1K: 0.01},
		},
	}

	log, _ := logger.Init(config.LoggingConfig{Level: "error", Format: "json"})
	p := NewProxy(cfg, log)

	st, ok := p.Router().Status("openai")
	if !ok {
		t.Fatal("router status for openai missing")
	}
	if st.Weight != 7 {
		t.Errorf("openai weight = %d, want 7 (from config)", st.Weight)
	}
	if st.CostPer1K != 0.03 {
		t.Errorf("openai cost = %f, want 0.03 (from config)", st.CostPer1K)
	}
	st, _ = p.Router().Status("anthropic")
	if st.Weight != 1 {
		t.Errorf("anthropic weight = %d, want 1 (default)", st.Weight)
	}
}

// TestProxy_CostStrategySelectsCheapest verifies the cost strategy
// end-to-end: the cheapest configured provider is tried first.
func TestProxy_CostStrategySelectsCheapest(t *testing.T) {
	t.Parallel()

	var openaiCalls, anthropicCalls int
	openai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		openaiCalls++
		w.WriteHeader(http.StatusOK)
	}))
	defer openai.Close()

	anthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		anthropicCalls++
		w.WriteHeader(http.StatusOK)
	}))
	defer anthropic.Close()

	cfg := &config.Config{
		Providers: &config.ProviderConfigs{
			OpenAI:    &config.ProviderConfig{BaseURL: openai.URL, APIKey: "k", CostPer1K: 0.03},
			Anthropic: &config.ProviderConfig{BaseURL: anthropic.URL, APIKey: "k", CostPer1K: 0.01},
		},
		Routing: &config.RoutingConfig{
			DefaultStrategy: "roundrobin",
			Routes: []config.RouteRule{
				{Model: "gpt-4", Providers: []string{"openai", "anthropic"}, Strategy: "cost"},
			},
		},
	}

	log, _ := logger.Init(config.LoggingConfig{Level: "error", Format: "json"})
	p := NewProxy(cfg, log)

	_, provider, err := p.routeAndForward(context.Background(), "gpt-4", canonicalFor("gpt-4"))
	if err != nil {
		t.Fatalf("routeAndForward() error = %v", err)
	}
	if provider != "anthropic" {
		t.Errorf("provider = %q, want anthropic (cheapest 0.01 < 0.03)", provider)
	}
	if openaiCalls != 0 {
		t.Errorf("openai calls = %d, want 0 (cost strategy picks cheapest first)", openaiCalls)
	}
	if anthropicCalls != 1 {
		t.Errorf("anthropic calls = %d, want 1", anthropicCalls)
	}
}

// TestProxy_RouteAndForward_BareModelBareForAdapter verifies the
// provider-prefixed model is stripped before the adapter sees it.
func TestProxy_RouteAndForward_BareModelForAdapter(t *testing.T) {
	t.Parallel()

	var receivedModel string
	openai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, 1024)
		n, _ := r.Body.Read(body)
		payload := string(body[:n])
		// Extract "model":"..." crudely for the assertion.
		if i := strings.Index(payload, `"model":"`); i >= 0 {
			rest := payload[i+len(`"model":"`):]
			if j := strings.Index(rest, `"`); j >= 0 {
				receivedModel = rest[:j]
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer openai.Close()

	p, _ := newRoutingProxy(t, openai.URL, openai.URL, nil)

	resp, _, err := p.routeAndForward(context.Background(), "openai/gpt-4", canonicalFor("gpt-4"))
	if err != nil {
		t.Fatalf("routeAndForward() error = %v", err)
	}
	defer resp.Body.Close()

	if receivedModel != "gpt-4" {
		t.Errorf("adapter received model = %q, want bare gpt-4 (prefix stripped)", receivedModel)
	}
}

// canonicalFor builds a minimal canonical request for tests.
func canonicalFor(model string) *provider.CanonicalRequest {
	return &provider.CanonicalRequest{Model: model}
}

// TestProxy_DefaultFailoverConfig verifies the proxy defaults to enabled
// failover with 3 providers when no routing config is present.
func TestProxy_DefaultFailoverConfig(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	log, _ := logger.Init(config.LoggingConfig{Level: "error", Format: "json"})
	p := NewProxy(cfg, log)

	if !p.failover.Enabled {
		t.Error("default failover must be enabled (D-10)")
	}
	if p.failover.MaxProviders != 3 {
		t.Errorf("default MaxProviders = %d, want 3", p.failover.MaxProviders)
	}
	if p.failover.ReturnOnError {
		t.Error("default ReturnOnError must be false (auto failover)")
	}
}

// TestProxy_RecordsFailuresPassively verifies failed provider attempts
// feed the router's passive health tracking.
func TestProxy_RecordsFailuresPassively(t *testing.T) {
	t.Parallel()

	openai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer openai.Close()

	anthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer anthropic.Close()

	p, _ := newRoutingProxy(t, openai.URL, anthropic.URL, nil)

	// RouteWithFailover records the failure via RecordFailure.
	_, _, err := p.routeAndForward(context.Background(), "gpt-4", canonicalFor("gpt-4"))
	if err != nil {
		t.Fatalf("routeAndForward() error = %v", err)
	}

	st, ok := p.Router().Status("openai")
	if !ok {
		t.Fatal("router status for openai missing")
	}
	if st.ErrorCount == 0 {
		t.Error("openai ErrorCount = 0, want the 502 failure recorded")
	}
}
