// Package httputil provides shared HTTP helpers used across the middleware
// chain (auth, cache, rate limiting, proxy).
package httputil

import (
	"bytes"
	"io"
	"net/http"
)

// DefaultMaxBodyBytes bounds how much of a request body a middleware may buffer
// to build cache keys or rate-limit dimensions. Chat-completion payloads are
// typically well under 1 MiB; the cap protects the gateway from unbounded reads
// that a malicious or buggy client could trigger.
const DefaultMaxBodyBytes int64 = 1 << 20

// PeekBody reads the request body (up to maxBytes) into memory, then restores it
// so downstream handlers can read it again. The body is buffered with
// io.Copy + bytes.Buffer so the read and the replay share one allocation.
//
// On success the returned bytes are the full body and r.Body now points at the
// in-memory buffer. On error (read failure or exceeding maxBytes) the original
// body is closed and the caller should pass the request through unpeeked.
func PeekBody(w http.ResponseWriter, r *http.Request, maxBytes int64) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}

	limited := http.MaxBytesReader(w, r.Body, maxBytes)
	var buf bytes.Buffer
	_, err := io.Copy(&buf, limited)
	_ = limited.Close() // close the original body on both paths
	if err != nil {
		return nil, err
	}

	r.Body = io.NopCloser(&buf)
	return buf.Bytes(), nil
}
