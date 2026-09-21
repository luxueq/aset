package openai

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/luxueq/aigateway/internal/common/config"
	"github.com/luxueq/aigateway/internal/provider"
)

const sampleOpenAIStream = `data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1694268300,"model":"gpt-4","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"},"finish_reason":null}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1694268300,"model":"gpt-4","choices":[{"index":0,"delta":{"content":" world"},"finish_reason":null}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1694268300,"model":"gpt-4","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":12,"total_tokens":21}}

data: [DONE]

`

func newTestProvider() *Provider {
	return NewOpenAIProvider(&config.ProviderConfig{
		BaseURL: "https://api.openai.com/v1",
		APIKey:  "test-key",
	})
}

// TestStreamAdapter_ParsesOpenAISSEFormat verifies the adapter parses
// "data: {\"choices\":[{\"delta\":{\"content\":\"...\"}}]}" events.
func TestStreamAdapter_ParsesOpenAISSEFormat(t *testing.T) {
	t.Parallel()

	p := newTestProvider()
	body := io.NopCloser(strings.NewReader(sampleOpenAIStream))

	ch := p.StreamAdapter(body)

	var contents []string
	var gotUsage bool
	var gotDone bool
	for chunk := range ch {
		if chunk.Delta != "" {
			contents = append(contents, chunk.Delta)
		}
		if chunk.Usage != nil {
			gotUsage = true
			if chunk.Usage.PromptTokens != 9 || chunk.Usage.CompletionTokens != 12 || chunk.Usage.TotalTokens != 21 {
				t.Errorf("usage = %+v, want 9/12/21", chunk.Usage)
			}
		}
		if chunk.Done {
			gotDone = true
		}
	}

	if got, want := strings.Join(contents, ""), "Hello world"; got != want {
		t.Errorf("joined deltas = %q, want %q", got, want)
	}
	if !gotUsage {
		t.Error("expected usage chunk to be parsed")
	}
	if !gotDone {
		t.Error("expected Done=true on final chunk")
	}
}

// TestStreamAdapter_EmitsDoneOnDoneSentinel verifies "data: [DONE]" is
// converted to a terminal chunk with Done=true.
func TestStreamAdapter_EmitsDoneOnDoneSentinel(t *testing.T) {
	t.Parallel()

	p := newTestProvider()
	body := io.NopCloser(strings.NewReader("data: [DONE]\n\n"))

	var chunks []*struct {
		done bool
	}
	for c := range p.StreamAdapter(body) {
		chunks = append(chunks, &struct{ done bool }{c.Done})
	}

	if len(chunks) != 1 || !chunks[0].done {
		t.Errorf("expected exactly one Done=true chunk, got %d chunks", len(chunks))
	}
}

// TestStreamAdapter_SkipsMalformedLines verifies malformed JSON is skipped.
func TestStreamAdapter_SkipsMalformedLines(t *testing.T) {
	t.Parallel()

	p := newTestProvider()
	body := io.NopCloser(strings.NewReader("data: {invalid json}\n\ndata: [DONE]\n\n"))

	var deltas int
	for c := range p.StreamAdapter(body) {
		if c.Delta != "" {
			deltas++
		}
	}

	if deltas != 0 {
		t.Errorf("expected 0 deltas from malformed stream, got %d", deltas)
	}
}

// TestStreamAdapter_BlocksAndCompletes verifies the channel semantics:
// chunks arrive in order and the channel closes when the stream ends.
func TestStreamAdapter_ChannelClosesOnEnd(t *testing.T) {
	t.Parallel()

	p := newTestProvider()
	body := io.NopCloser(strings.NewReader(sampleOpenAIStream))

	ch := p.StreamAdapter(body)
	timeout := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return // channel closed — stream finished
			}
		case <-timeout:
			t.Fatal("channel did not close within 2s — goroutine leak or blocked send")
		}
	}
}

// TestConvertRequest_BuildsHTTPRequest verifies headers and target URL.
func TestConvertRequest_BuildsHTTPRequest(t *testing.T) {
	t.Parallel()

	p := newTestProvider()
	req := canonicalRequestWithMessages(t)

	httpReq, err := p.ConvertRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("ConvertRequest failed: %v", err)
	}

	if httpReq.Method != http.MethodPost {
		t.Errorf("method = %s, want POST", httpReq.Method)
	}
	if got := httpReq.URL.String(); got != "https://api.openai.com/v1/chat/completions" {
		t.Errorf("url = %s, want https://api.openai.com/v1/chat/completions", got)
	}
	if got := httpReq.Header.Get("Authorization"); got != "Bearer test-key" {
		t.Errorf("Authorization = %q, want %q", got, "Bearer test-key")
	}
	if got := httpReq.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
}

// TestConvertRequest_ReplaysBody verifies GetBody is set so retries can
// replay the request body.
func TestConvertRequest_ReplaysBody(t *testing.T) {
	t.Parallel()

	p := newTestProvider()
	httpReq, err := p.ConvertRequest(context.Background(), canonicalRequestWithMessages(t))
	if err != nil {
		t.Fatalf("ConvertRequest failed: %v", err)
	}

	if httpReq.GetBody == nil {
		t.Fatal("GetBody is nil — retry cannot replay request body")
	}
}

func canonicalRequestWithMessages(t *testing.T) *provider.CanonicalRequest {
	t.Helper()

	temp := 0.7
	maxTok := 100
	return &provider.CanonicalRequest{
		Model:       "gpt-4",
		Messages:    []provider.Message{{Role: "user", Content: "Hello"}},
		Stream:      true,
		Temperature: &temp,
		MaxTokens:   &maxTok,
	}
}
