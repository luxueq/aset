package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/luxueq/aigateway/internal/common/config"
	"github.com/luxueq/aigateway/internal/common/logger"
)

// flushRecorder is a streaming-safe ResponseWriter: writes append to a
// mutex-guarded buffer so tests can observe partial output mid-stream.
type flushRecorder struct {
	mu      sync.Mutex
	buf     strings.Builder
	hdr     http.Header
	code    int
	flushes int
}

func (r *flushRecorder) Header() http.Header  { return r.hdr }
func (r *flushRecorder) WriteHeader(code int) { r.code = code }
func (r *flushRecorder) Flush()               { r.mu.Lock(); r.flushes++; r.mu.Unlock() }
func (r *flushRecorder) Write(b []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.Write(b)
}

// TestStreamResponse_Passthrough verifies SSE bytes from the upstream
// response body are written through to the client unchanged.
func TestStreamResponse_Passthrough(t *testing.T) {
	t.Parallel()

	upstream := "data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\ndata: [DONE]\n\n"
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(upstream)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}

	rec := httptest.NewRecorder()
	log, _ := logger.Init(config.LoggingConfig{Level: "error", Format: "json"})
	p := &Proxy{logger: log}

	// Mirror the ServeHTTP call sequence: upstream headers are copied to
	// the client before streaming begins.
	for key, values := range resp.Header {
		for _, value := range values {
			rec.Header().Add(key, value)
		}
	}

	p.streamResponse(rec, resp)

	if got := rec.Body.String(); got != upstream {
		t.Errorf("streamed body = %q, want upstream passthrough %q", got, upstream)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q, want text/event-stream (copied from upstream by caller)", ct)
	}
}

// TestStreamResponse_FlushesPerChunk verifies each read chunk is flushed
// immediately rather than buffered until stream end.
func TestStreamResponse_FlushesPerChunk(t *testing.T) {
	t.Parallel()

	// A slow reader upstream: chunks arrive with delays. The proxy must
	// write+flush each chunk as it arrives, observable via write timing.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for _, chunk := range []string{"data: one\n\n", "data: two\n\n", "data: [DONE]\n\n"} {
			io.WriteString(w, chunk)
			flusher.Flush()
			time.Sleep(30 * time.Millisecond)
		}
	}))
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL)
	if err != nil {
		t.Fatalf("failed to reach upstream: %v", err)
	}
	defer resp.Body.Close()

	log, _ := logger.Init(config.LoggingConfig{Level: "error", Format: "json"})
	p := &Proxy{httpClient: srv.Client(), logger: log}

	rec := newFlushRecorder()
	done := make(chan struct{})
	go func() {
		p.streamResponse(rec, resp)
		close(done)
	}()

	// After ~65ms (two chunks sent + margins) we should already have
	// partial output — proving flush-per-chunk, not buffer-until-end.
	deadline := time.After(100 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("stream finished too early — upstream still sending")
	case <-time.After(65 * time.Millisecond):
	case <-deadline:
	}

	rec.mu.Lock()
	partial := rec.buf.String()
	rec.mu.Unlock()

	if !strings.Contains(partial, "data: one") || !strings.Contains(partial, "data: two") {
		t.Errorf("partial output after 65ms = %q, want first two chunks already flushed", partial)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("streamResponse did not finish within 2s")
	}
}

// newFlushRecorder builds a ResponseWriter that records writes while
// allowing concurrent observation of the buffer (streaming test needs to
// read the buffer before the handler returns, which httptest.Recorder
// does not support safely).
func newFlushRecorder() *flushRecorder {
	return &flushRecorder{hdr: http.Header{}}
}
