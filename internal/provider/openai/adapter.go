package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/luxueq/aigateway/internal/common/config"
	"github.com/luxueq/aigateway/internal/provider"
)

// Provider implements the provider.Provider interface for OpenAI.
type Provider struct {
	baseURL string
	apiKey  string
}

// NewOpenAIProvider creates a new OpenAI provider.
func NewOpenAIProvider(cfg *config.ProviderConfig) *Provider {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	return &Provider{
		baseURL: baseURL,
		apiKey:  cfg.APIKey,
	}
}

func (p *Provider) Name() string {
	return "openai"
}

func (p *Provider) ConvertRequest(ctx context.Context, req *provider.CanonicalRequest) (*http.Request, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

	return httpReq, nil
}

func (p *Provider) StreamAdapter(body provider.ReadCloser) <-chan *provider.CanonicalChunk {
	ch := make(chan *provider.CanonicalChunk)

	go func() {
		defer close(ch)
		defer body.Close()

		scanner := bufio.NewScanner(body)
		for scanner.Scan() {
			line := scanner.Text()

			// OpenAI SSE format: "data: {...}\n\n" or "data: [DONE]"
			if !strings.HasPrefix(line, "data: ") {
				continue
			}

			data := strings.TrimPrefix(line, "data: ")

			// Check for stream end
			if data == "[DONE]" {
				ch <- &provider.CanonicalChunk{
					Done: true,
				}
				return
			}

			// Parse the JSON chunk
			var openAIChunk openAIStreamChunk
			if err := json.Unmarshal([]byte(data), &openAIChunk); err != nil {
				continue // skip malformed chunks
			}

			chunk := &provider.CanonicalChunk{
				ID:      openAIChunk.ID,
				RawData: []byte(data),
			}

			// Extract delta content
			if len(openAIChunk.Choices) > 0 {
				chunk.Delta = openAIChunk.Choices[0].Delta.Content
				if openAIChunk.Choices[0].FinishReason != nil {
					chunk.Done = true
				}
			}

			// Extract usage if present (usually in last chunk)
			if openAIChunk.Usage != nil {
				chunk.Usage = &provider.Usage{
					PromptTokens:     openAIChunk.Usage.PromptTokens,
					CompletionTokens: openAIChunk.Usage.CompletionTokens,
					TotalTokens:      openAIChunk.Usage.TotalTokens,
				}
			}

			ch <- chunk
		}
	}()

	return ch
}

// OpenAI-specific types for parsing
type openAIStreamChunk struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index        int `json:"index"`
		Delta        struct {
			Role    string `json:"role,omitempty"`
			Content string `json:"content,omitempty"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason,omitempty"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage,omitempty"`
}

// Verify Provider implements provider.Provider
var _ provider.Provider = (*Provider)(nil)
