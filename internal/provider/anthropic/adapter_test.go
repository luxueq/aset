package anthropic

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/luxueq/aigateway/internal/common/config"
	"github.com/luxueq/aigateway/internal/provider"
)

const sampleAnthropicStream = `event: message_start
data: {"type":"message_start","message":{"id":"msg_01XFDUDYJgAACzvnptvVoYEL","usage":{"input_tokens":25}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":18}}

event: message_stop
data: {"type":"message_stop"}
`

func newTestProvider() *Provider {
	return NewAnthropicProvider(&config.ProviderConfig{
		BaseURL: "https://api.anthropic.com/v1",
		APIKey:  "test-key",
	})
}

// TestConvertRequest_ExtractsSystemMessage verifies the system message is
// moved out of the messages array into the top-level system field, and the
// remaining messages preserve their order.
func TestConvertRequest_ExtractsSystemMessage(t *testing.T) {
	t.Parallel()

	p := newTestProvider()
	req := &provider.CanonicalRequest{
		Model: "claude-3-opus",
		Messages: []provider.Message{
			{Role: "system", Content: "You are helpful."},
			{Role: "user", Content: "Hi"},
		},
	}

	httpReq, err := p.ConvertRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("ConvertRequest failed: %v", err)
	}

	body, err := io.ReadAll(httpReq.Body)
	if err != nil {
		t.Fatalf("failed to read request body: %v", err)
	}

	// Unmarshal into the adapter's own request type via JSON round-trip.
	var payload map[string]interface{}
	if err := jsonUnmarshal(body, &payload); err != nil {
		t.Fatalf("failed to parse body: %v — body: %s", err, body)
	}

	if got, want := payload["system"], "You are helpful."; got != want {
		t.Errorf("system = %v, want %q", got, want)
	}

	msgs, _ := payload["messages"].([]interface{})
	if len(msgs) != 1 {
		t.Fatalf("messages length = %d, want 1 (system removed)", len(msgs))
	}
	first, _ := msgs[0].(map[string]interface{})
	if first["role"] != "user" || first["content"] != "Hi" {
		t.Errorf("messages[0] = %v, want user/Hi", first)
	}
}

// TestConvertRequest_NoSystemMessage verifies requests without a system
// message produce no system field.
func TestConvertRequest_NoSystemMessage(t *testing.T) {
	t.Parallel()

	p := newTestProvider()
	req := &provider.CanonicalRequest{
		Model:    "claude-3-opus",
		Messages: []provider.Message{{Role: "user", Content: "Hi"}},
	}

	httpReq, err := p.ConvertRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("ConvertRequest failed: %v", err)
	}

	body, _ := io.ReadAll(httpReq.Body)
	var payload map[string]interface{}
	if err := jsonUnmarshal(body, &payload); err != nil {
		t.Fatalf("failed to parse body: %v", err)
	}

	if _, ok := payload["system"]; ok {
		t.Error("system field should be absent when no system message")
	}
}

// TestConvertRequest_MergesMultipleSystemMessages verifies multiple system
// messages are merged into a single system field.
func TestConvertRequest_MergesMultipleSystemMessages(t *testing.T) {
	t.Parallel()

	p := newTestProvider()
	req := &provider.CanonicalRequest{
		Model: "claude-3-opus",
		Messages: []provider.Message{
			{Role: "system", Content: "Be terse."},
			{Role: "user", Content: "Hi"},
			{Role: "system", Content: "Always answer in English."},
		},
	}

	httpReq, err := p.ConvertRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("ConvertRequest failed: %v", err)
	}

	body, _ := io.ReadAll(httpReq.Body)
	var payload map[string]interface{}
	if err := jsonUnmarshal(body, &payload); err != nil {
		t.Fatalf("failed to parse body: %v", err)
	}

	if got, want := payload["system"], "Be terse.\n\nAlways answer in English."; got != want {
		t.Errorf("system = %q, want %q", got, want)
	}

	msgs, _ := payload["messages"].([]interface{})
	if len(msgs) != 1 {
		t.Errorf("messages length = %d, want 1", len(msgs))
	}
}

// TestConvertRequest_DefaultsMaxTokens verifies Anthropic's required
// max_tokens gets a default when the canonical request omits it.
func TestConvertRequest_DefaultsMaxTokens(t *testing.T) {
	t.Parallel()

	p := newTestProvider()
	req := &provider.CanonicalRequest{
		Model:    "claude-3-opus",
		Messages: []provider.Message{{Role: "user", Content: "Hi"}},
	}

	httpReq, err := p.ConvertRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("ConvertRequest failed: %v", err)
	}

	body, _ := io.ReadAll(httpReq.Body)
	var payload map[string]interface{}
	if err := jsonUnmarshal(body, &payload); err != nil {
		t.Fatalf("failed to parse body: %v", err)
	}

	if got := payload["max_tokens"]; got != float64(4096) {
		t.Errorf("max_tokens = %v, want 4096 default", got)
	}
}

// TestConvertRequest_SetsAnthropicHeaders verifies auth and version headers.
func TestConvertRequest_SetsAnthropicHeaders(t *testing.T) {
	t.Parallel()

	p := newTestProvider()
	req := &provider.CanonicalRequest{
		Model:    "claude-3-opus",
		Messages: []provider.Message{{Role: "user", Content: "Hi"}},
	}

	httpReq, err := p.ConvertRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("ConvertRequest failed: %v", err)
	}

	if got := httpReq.Header.Get("x-api-key"); got != "test-key" {
		t.Errorf("x-api-key = %q, want test-key", got)
	}
	if got := httpReq.Header.Get("anthropic-version"); got != DefaultAPIVersion {
		t.Errorf("anthropic-version = %q, want %q", got, DefaultAPIVersion)
	}
	if got := httpReq.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	if got := httpReq.URL.String(); got != "https://api.anthropic.com/v1/messages" {
		t.Errorf("url = %s, want https://api.anthropic.com/v1/messages", got)
	}
}

// TestStreamAdapter_ConvertsAnthropicEventsToCanonical verifies:
// - content_block_delta text_delta → Delta
// - message_start usage.input_tokens → Usage.PromptTokens
// - message_delta usage.output_tokens → Usage.CompletionTokens
// - message_delta stop_reason / message_stop → Done
func TestStreamAdapter_ConvertsAnthropicEventsToCanonical(t *testing.T) {
	t.Parallel()

	p := newTestProvider()
	body := io.NopCloser(strings.NewReader(sampleAnthropicStream))

	var contents []string
	var promptTokens, completionTokens int
	var gotDone bool

	for chunk := range p.StreamAdapter(body) {
		if chunk.Delta != "" {
			contents = append(contents, chunk.Delta)
		}
		if chunk.Usage != nil {
			if chunk.Usage.PromptTokens > 0 {
				promptTokens = chunk.Usage.PromptTokens
			}
			if chunk.Usage.CompletionTokens > 0 {
				completionTokens = chunk.Usage.CompletionTokens
			}
		}
		if chunk.Done {
			gotDone = true
		}
	}

	if got, want := strings.Join(contents, ""), "Hello world"; got != want {
		t.Errorf("joined deltas = %q, want %q", got, want)
	}
	if promptTokens != 25 {
		t.Errorf("PromptTokens = %d, want 25 (input_tokens → prompt_tokens)", promptTokens)
	}
	if completionTokens != 18 {
		t.Errorf("CompletionTokens = %d, want 18 (output_tokens → completion_tokens)", completionTokens)
	}
	if !gotDone {
		t.Error("expected Done=true on message_stop")
	}
}

// TestStreamAdapter_ChannelClosesOnEnd verifies the channel closes at
// stream end (no goroutine leak).
func TestStreamAdapter_ChannelClosesOnEnd(t *testing.T) {
	t.Parallel()

	p := newTestProvider()
	body := io.NopCloser(strings.NewReader(sampleAnthropicStream))

	ch := p.StreamAdapter(body)
	timeout := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-timeout:
			t.Fatal("channel did not close within 2s — goroutine leak or blocked send")
		}
	}
}

// jsonUnmarshal isolates the JSON decoding for readability.
func jsonUnmarshal(data []byte, v interface{}) error {
	return json.Unmarshal(data, v)
}
