package gateway

import (
	"bytes"
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
	httpClient *http.Client
	config     *config.Config
	logger     *zap.Logger
	providers  map[string]provider.Provider
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
		config:    cfg,
		logger:    log,
		providers: make(map[string]provider.Provider),
	}

	// Register default providers
	if cfg.Providers.OpenAI != nil {
		p.providers["openai"] = openai.NewOpenAIProvider(cfg.Providers.OpenAI)
	}
	if cfg.Providers.Anthropic != nil {
		p.providers["anthropic"] = anthropic.NewAnthropicProvider(cfg.Providers.Anthropic)
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

	// Forward request to provider
	resp, err := p.httpClient.Do(httpReq)
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

// Helper to create request body from canonical request
func buildRequestBody(canonical *provider.CanonicalRequest) ([]byte, error) {
	body := map[string]interface{}{
		"model":    canonical.Model,
		"messages": canonical.Messages,
	}
	if canonical.Stream {
		body["stream"] = true
	}
	if canonical.Temperature != nil {
		body["temperature"] = *canonical.Temperature
	}
	if canonical.MaxTokens != nil {
		body["max_tokens"] = *canonical.MaxTokens
	}
	return json.Marshal(body)
}

// Ensure Proxy implements http.Handler
var _ http.Handler = (*Proxy)(nil)

// io.ReadAll helper (to avoid importing io twice in the example)
func readAll(r io.Reader) ([]byte, error) {
	var buf bytes.Buffer
	_, err := io.Copy(&buf, r)
	return buf.Bytes(), err
}
