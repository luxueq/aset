package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/luxueq/aigateway/internal/common/config"
	"github.com/luxueq/aigateway/internal/provider"
	"github.com/luxueq/aigateway/internal/provider/anthropic"
	"github.com/luxueq/aigateway/internal/provider/openai"
	"go.uber.org/zap"
)

// Proxy handles HTTP requests and forwards them to AI model providers.
type Proxy struct {
	httpClient  *http.Client
	config      *config.Config
	logger      *zap.Logger
	providers   map[string]provider.Provider
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

	return p
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

	providerName, actualModel := parseModel(modelName)

	prov, ok := p.providers[providerName]
	if !ok {
		p.writeError(w, http.StatusBadRequest, "invalid_request",
			"unknown provider: "+providerName)
		return
	}

	p.logger.Info("proxying request",
		zap.String("provider", providerName),
		zap.String("model", actualModel),
		zap.String("method", r.Method),
		zap.String("path", r.URL.Path),
	)

	// Convert to canonical request and then to provider-specific request
	canonical := &provider.CanonicalRequest{
		Model:    actualModel,
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

	// Convert to provider-specific request
	httpReq, err := prov.ConvertRequest(r.Context(), canonical)
	if err != nil {
		p.writeError(w, http.StatusInternalServerError, "provider_error",
			"failed to convert request: "+err.Error())
		return
	}

	// Forward request to provider with retry
	resp, err := p.doWithRetry(r.Context(), httpReq)
	if err != nil {
		p.logger.Error("upstream request failed",
			zap.String("provider", providerName),
			zap.Error(err),
		)
		p.writeError(w, http.StatusBadGateway, "provider_error",
			"upstream request failed: "+err.Error())
		return
	}
	defer resp.Body.Close()

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

// parseModel extracts provider name and model name from "provider/model" format.
func parseModel(model string) (providerName, modelName string) {
	parts := strings.SplitN(model, "/", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "openai", model // Default to OpenAI
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
