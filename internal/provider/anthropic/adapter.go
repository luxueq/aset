package anthropic

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

// Provider implements the provider.Provider interface for Anthropic.
type Provider struct {
	baseURL string
	apiKey  string
}

// NewAnthropicProvider creates a new Anthropic provider.
func NewAnthropicProvider(cfg *config.ProviderConfig) *Provider {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://api.anthropic.com/v1"
	}
	return &Provider{
		baseURL: baseURL,
		apiKey:  cfg.APIKey,
	}
}

func (p *Provider) Name() string {
	return "anthropic"
}

func (p *Provider) ConvertRequest(ctx context.Context, req *provider.CanonicalRequest) (*http.Request, error) {
	// Convert canonical request to Anthropic format
	anthropicReq := p.convertToAnthropic(req)

	body, err := json.Marshal(anthropicReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.baseURL+"/messages", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", p.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	return httpReq, nil
}

func (p *Provider) convertToAnthropic(req *provider.CanonicalRequest) *anthropicRequest {
	anthReq := &anthropicRequest{
		Model:   req.Model,
		Stream:  req.Stream,
	}

	// Extract system message
	for _, msg := range req.Messages {
		if msg.Role == "system" {
			anthReq.System = msg.Content
			continue
		}
		anthReq.Messages = append(anthReq.Messages, anthropicMessage{
			Role:    msg.Role,
			Content: msg.Content,
		})
	}

	if req.MaxTokens != nil {
		anthReq.MaxTokens = *req.MaxTokens
	} else {
		anthReq.MaxTokens = 4096 // Anthropic requires max_tokens
	}

	if req.Temperature != nil {
		anthReq.Temperature = *req.Temperature
	}

	return anthReq
}

func (p *Provider) StreamAdapter(body provider.ReadCloser) <-chan *provider.CanonicalChunk {
	ch := make(chan *provider.CanonicalChunk)

	go func() {
		defer close(ch)
		defer body.Close()

		scanner := bufio.NewScanner(body)

		for scanner.Scan() {
			line := scanner.Text()

			// Anthropic SSE format:
			// event: message_start
			// data: {"type":"message_start",...}
			if strings.HasPrefix(line, "event: ") {
				continue
			}

			if !strings.HasPrefix(line, "data: ") {
				continue
			}

			data := strings.TrimPrefix(line, "data: ")

			var event anthropicStreamEvent
			if err := json.Unmarshal([]byte(data), &event); err != nil {
				continue
			}

			chunk := &provider.CanonicalChunk{
				RawData: []byte(data),
			}

			switch event.Type {
			case "content_block_delta":
				if event.Delta.Type == "text_delta" {
					chunk.Delta = event.Delta.Text
				}
			case "message_delta":
				if event.Delta.StopReason != "" {
					chunk.Done = true
				}
				if event.Usage != nil {
					chunk.Usage = &provider.Usage{
						CompletionTokens: event.Usage.OutputTokens,
					}
				}
			case "message_start":
				if event.Message.Usage != nil {
					chunk.Usage = &provider.Usage{
						PromptTokens: event.Message.Usage.InputTokens,
					}
				}
			case "message_stop":
				chunk.Done = true
			}

			ch <- chunk
		}
	}()

	return ch
}

// Anthropic-specific types

type anthropicRequest struct {
	Model       string             `json:"model"`
	Messages    []anthropicMessage `json:"messages"`
	System      string             `json:"system,omitempty"`
	MaxTokens   int                `json:"max_tokens"`
	Stream      bool               `json:"stream,omitempty"`
	Temperature float64            `json:"temperature,omitempty"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicStreamEvent struct {
	Type    string `json:"type"`
	Message struct {
		Usage *struct {
			InputTokens int `json:"input_tokens"`
		} `json:"usage,omitempty"`
	} `json:"message,omitempty"`
	Delta struct {
		Type       string `json:"type"`
		Text       string `json:"text,omitempty"`
		StopReason string `json:"stop_reason,omitempty"`
	} `json:"delta,omitempty"`
	Usage *struct {
		OutputTokens int `json:"output_tokens"`
	} `json:"usage,omitempty"`
}

// Verify Provider implements provider.Provider
var _ provider.Provider = (*Provider)(nil)
