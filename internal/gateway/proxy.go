package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/luxueq/aigateway/internal/common/config"
	"github.com/luxueq/aigateway/internal/provider"
	"github.com/luxueq/aigateway/internal/provider/anthropic"
	"github.com/luxueq/aigateway/internal/provider/openai"
	"github.com/luxueq/aigateway/internal/router"
	"go.uber.org/zap"
)

// Proxy handles HTTP requests and forwards them to AI model providers.
type Proxy struct {
	httpClient  *http.Client
	config      *config.Config
	logger      *zap.Logger
	providers   map[string]provider.Provider
	rt          *router.Router
	failover    router.FailoverConfig
	retryConfig RetryConfig
}

// NewProxy creates a new Proxy instance.
func NewProxy(cfg *config.Config, log *zap.Logger) *Proxy {
	p := &Proxy{
		httpClient: &http.Client{
			Timeout: 120 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 20,
				IdleConnTimeout:     90 * time.Second,
			},
		},
		config:      cfg,
		logger:      log,
		providers:   make(map[string]provider.Provider),
		failover:    router.DefaultFailoverConfig(),
		retryConfig: DefaultRetryConfig(),
	}

	// Register configured providers
	if cfg.Providers != nil {
		if cfg.Providers.OpenAI != nil {
			p.providers["openai"] = openai.NewOpenAIProvider(cfg.Providers.OpenAI)
		}
		if cfg.Providers.Anthropic != nil {
			p.providers["anthropic"] = anthropic.NewAnthropicProvider(cfg.Providers.Anthropic)
		}
	}

	p.initRouter(cfg)

	return p
}

// initRouter builds the routing engine from config: provider status
// entries (weight, cost), route rules, and failover settings.
func (p *Proxy) initRouter(cfg *config.Config) {
	rcfg := router.Config{}
	if cfg.Routing != nil {
		rcfg.DefaultStrategy = cfg.Routing.DefaultStrategy
		rcfg.DefaultProvider = cfg.Routing.DefaultProvider
		for _, rule := range cfg.Routing.Routes {
			rcfg.Routes = append(rcfg.Routes, router.RouteRule{
				Model:     rule.Model,
				Providers: rule.Providers,
				Strategy:  rule.Strategy,
			})
		}
		if f := cfg.Routing.Failover; f != nil {
			p.failover = router.FailoverConfig{
				Enabled:       f.Enabled,
				MaxProviders:  f.MaxProviders,
				ReturnOnError: f.ReturnOnError,
			}
		}
	}

	p.rt = router.New(rcfg)

	// Seed provider runtime status from provider config.
	var statuses []*router.ProviderStatus
	for name := range p.providers {
		statuses = append(statuses, p.providerStatus(name))
	}
	p.rt.SetProviders(statuses)
}

// providerStatus builds the initial ProviderStatus for a configured
// provider, pulling weight and cost hints from config.
func (p *Proxy) providerStatus(name string) *router.ProviderStatus {
	st := &router.ProviderStatus{
		Name:    name,
		Healthy: true,
		Weight:  1,
	}
	if p.config.Providers != nil {
		for _, pc := range p.config.Providers.All() {
			if pc.Config == nil {
				continue
			}
			if pc.Name == name {
				if pc.Config.Weight > 0 {
					st.Weight = pc.Config.Weight
				}
				st.CostPer1K = pc.Config.CostPer1K
				break
			}
		}
	}
	return st
}

// Router exposes the routing engine (for health monitor wiring and tests).
func (p *Proxy) Router() *router.Router {
	return p.rt
}

// ServeHTTP handles incoming API requests.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Parse the request to determine the model/provider
	var reqBody map[string]interface{}
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		p.writeError(w, http.StatusBadRequest, "invalid_request", "failed to read request body")
		return
	}

	if err := json.Unmarshal(bodyBytes, &reqBody); err != nil {
		p.writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}

	// Extract model name (format: "provider/model" or just "model")
	modelName, ok := reqBody["model"].(string)
	if !ok || modelName == "" {
		p.writeError(w, http.StatusBadRequest, "invalid_request", "model field is required")
		return
	}

	// Convert to canonical request
	canonical := &provider.CanonicalRequest{
		Messages: extractMessages(reqBody),
		Stream:   isStreaming(reqBody),
	}
	if temp, ok := reqBody["temperature"].(float64); ok {
		t := temp
		canonical.Temperature = &t
	}
	if maxTok, ok := reqBody["max_tokens"].(float64); ok {
		mt := int(maxTok)
		canonical.MaxTokens = &mt
	}

	// Route (with failover) and forward to the selected provider.
	resp, providerName, err := p.routeAndForward(r.Context(), modelName, canonical)
	if err != nil {
		status := http.StatusBadGateway
		errType := "provider_error"
		switch {
		case errors.Is(err, router.ErrAllProvidersFailed):
			errType = "server_error"
		case errors.Is(err, router.ErrNoHealthyProvider):
			status = http.StatusServiceUnavailable
			errType = "service_unavailable"
		}
		p.logger.Error("request routing failed",
			zap.String("model", modelName),
			zap.Error(err),
		)
		p.writeError(w, status, errType, err.Error())
		return
	}
	defer resp.Body.Close()

	p.logger.Info("proxying request",
		zap.String("provider", providerName),
		zap.String("model", canonical.Model),
		zap.String("method", r.Method),
		zap.String("path", r.URL.Path),
	)

	// Set response headers
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.Header().Set("X-Provider", providerName)
	w.Header().Set("X-Cache", "MISS")

	// Stream response back
	if canonical.Stream {
		p.streamResponse(w, resp)
	} else {
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}
}

// routeAndForward selects a provider via the routing engine (with
// failover) and forwards the canonical request to it.
// Returns the upstream response, the provider that served it, or a
// terminal error (wrapped ErrAllProvidersFailed or ErrNoHealthyProvider).
func (p *Proxy) routeAndForward(ctx context.Context, model string, canonical *provider.CanonicalRequest) (*http.Response, string, error) {
	// Adapters expect the bare model name; the router consumes the full
	// "provider/model" string for pool resolution.
	canonicalForProvider := *canonical
	canonicalForProvider.Model = bareModel(model)

	var lastResp *http.Response
	var respProvider string

	_, _, err := p.rt.RouteWithFailover(model, p.failover, func(c *router.ProviderStatus) error {
		prov, ok := p.providers[c.Name]
		if !ok {
			return fmt.Errorf("provider %q is not configured", c.Name)
		}

		httpReq, err := prov.ConvertRequest(ctx, &canonicalForProvider)
		if err != nil {
			return fmt.Errorf("failed to convert request for %s: %w", c.Name, err)
		}

		resp, err := p.doWithRetry(ctx, httpReq)
		if err != nil {
			return fmt.Errorf("upstream request to %s failed: %w", c.Name, err)
		}

		// 5xx/429 responses are failover-worthy provider failures when
		// auto-failover is on; anything else (2xx, 4xx) is a final result.
		if p.failover.Enabled && !p.failover.ReturnOnError &&
			(resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests) {
			resp.Body.Close()
			return &providerFailure{provider: c.Name, status: resp.StatusCode}
		}

		lastResp = resp
		respProvider = c.Name
		return nil
	})

	if lastResp != nil {
		return lastResp, respProvider, nil
	}

	if err != nil {
		var pf *providerFailure
		if errors.As(err, &pf) {
			// The winning failure detail is kept, but the terminal
			// outcome for the client is failover exhaustion.
			return nil, "", fmt.Errorf("provider %s returned status %d: %w", pf.provider, pf.status, router.ErrAllProvidersFailed)
		}
		return nil, "", err
	}

	return nil, "", fmt.Errorf("no provider served the request")
}

// providerFailure marks a provider HTTP-level failure inside the
// failover callback so the final error can be reported accurately.
type providerFailure struct {
	provider string
	status   int
}

func (e *providerFailure) Error() string {
	return fmt.Sprintf("provider %s returned status %d", e.provider, e.status)
}

// bareModel strips an explicit "provider/" prefix from a model string.
func bareModel(model string) string {
	if i := strings.Index(model, "/"); i >= 0 {
		return model[i+1:]
	}
	return model
}

func (p *Proxy) streamResponse(w http.ResponseWriter, resp *http.Response) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		p.writeError(w, http.StatusInternalServerError, "internal_error",
			"streaming not supported")
		return
	}

	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			w.Write(buf[:n])
			flusher.Flush()
		}
		if err != nil {
			if err != io.EOF {
				p.logger.Warn("stream read error", zap.Error(err))
			}
			break
		}
	}
}

// doWithRetry executes an HTTP request with automatic retry on retryable failures.
func (p *Proxy) doWithRetry(ctx context.Context, req *http.Request) (*http.Response, error) {
	cfg := p.retryConfig

	var lastResp *http.Response
	var lastErr error

	for attempt := 0; attempt <= cfg.MaxRetries; attempt++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		// Clone request body for retry (original body is consumed after Do)
		if attempt > 0 {
			if req.GetBody == nil {
				p.logger.Warn("cannot retry: request body not replayable",
					zap.Int("attempt", attempt))
				return lastResp, lastErr
			}
			body, err := req.GetBody()
			if err != nil {
				return lastResp, lastErr
			}
			req.Body = body
		}

		resp, err := p.httpClient.Do(req)
		if err != nil {
			lastErr = err
			p.logger.Warn("upstream request failed",
				zap.Int("attempt", attempt+1),
				zap.Int("max_retries", cfg.MaxRetries),
				zap.Error(err))
		} else {
			// Non-retryable status: return immediately
			if !cfg.shouldRetry(resp.StatusCode) {
				return resp, nil
			}
			// Retryable status: close body and retry
			lastResp = resp
			p.logger.Warn("upstream returned retryable status",
				zap.Int("status", resp.StatusCode),
				zap.Int("attempt", attempt+1),
				zap.Int("max_retries", cfg.MaxRetries))
			resp.Body.Close()
		}

		// Wait before next retry (skip on last attempt)
		if attempt < cfg.MaxRetries {
			backoff := cfg.backoff(attempt)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return lastResp, nil
}

func (p *Proxy) writeError(w http.ResponseWriter, status int, errType, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]interface{}{
			"message": message,
			"type":    errType,
		},
	})
}

func extractMessages(body map[string]interface{}) []provider.Message {
	msgsRaw, ok := body["messages"].([]interface{})
	if !ok {
		return nil
	}

	var messages []provider.Message
	for _, m := range msgsRaw {
		msgMap, ok := m.(map[string]interface{})
		if !ok {
			continue
		}
		role, _ := msgMap["role"].(string)
		content, _ := msgMap["content"].(string)
		messages = append(messages, provider.Message{
			Role:    role,
			Content: content,
		})
	}
	return messages
}

func isStreaming(body map[string]interface{}) bool {
	stream, ok := body["stream"].(bool)
	return ok && stream
}

// Ensure Proxy implements http.Handler
var _ http.Handler = (*Proxy)(nil)
