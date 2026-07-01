package provider

import (
	"context"
	"net/http"
)

// Provider defines the interface for AI model providers.
type Provider interface {
	// Name returns the provider name (e.g., "openai", "anthropic").
	Name() string

	// ConvertRequest converts a canonical request to a provider-specific HTTP request.
	ConvertRequest(ctx context.Context, req *CanonicalRequest) (*http.Request, error)

	// StreamAdapter parses a streaming response body into canonical chunks.
	// Returns a channel that emits chunks and closes when the stream ends.
	StreamAdapter(body ReadCloser) <-chan *CanonicalChunk
}

// ReadCloser is an alias for io.ReadCloser to avoid circular imports.
type ReadCloser interface {
	Read(p []byte) (n int, err error)
	Close() error
}

// CanonicalRequest represents a normalized request across all providers.
type CanonicalRequest struct {
	Model       string     `json:"model"`
	Messages    []Message  `json:"messages"`
	Stream      bool       `json:"stream,omitempty"`
	Temperature *float64   `json:"temperature,omitempty"`
	MaxTokens   *int       `json:"max_tokens,omitempty"`
	TopP        *float64   `json:"top_p,omitempty"`
	Stop        []string   `json:"stop,omitempty"`
	Seed        *int       `json:"seed,omitempty"`
}

// Message represents a chat message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// CanonicalResponse represents a normalized non-streaming response.
type CanonicalResponse struct {
	ID      string   `json:"id"`
	Choices []Choice `json:"choices"`
	Usage   *Usage   `json:"usage,omitempty"`
	Model   string   `json:"model"`
}

// Choice represents a single response choice.
type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

// CanonicalChunk represents a single streaming chunk.
type CanonicalChunk struct {
	ID      string  `json:"id"`
	Delta   string  `json:"delta"`
	Usage   *Usage  `json:"usage,omitempty"`
	Done    bool    `json:"done"`
	RawData []byte  `json:"-"` // original provider data for passthrough
}

// Usage represents token usage information.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}
