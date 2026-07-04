package proxy

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
)

// MaxRequestBodyBytes caps how much of an incoming request body Conduit
// will buffer in memory to inspect it (plugin hooks, rate-limit tool-name
// extraction). It is not a limit on streamed SSE traffic, which is never
// buffered (see sse.go).
const MaxRequestBodyBytes = 10 * 1024 * 1024 // 10MB

// ReadAndReplaceBody reads r.Body fully (up to MaxRequestBodyBytes), then
// replaces it with a fresh reader over the same bytes so downstream
// handlers can still read it. http.Request.Body is a single-use
// io.ReadCloser; any middleware that needs to inspect the body — without
// consuming it for everyone downstream — must do this dance. Exported so
// internal/ratelimit and internal/auth's middlewares (which run earlier in
// the chain than the proxy handler that would otherwise do this once) can
// share the same body-peeking logic instead of reimplementing it.
func ReadAndReplaceBody(r *http.Request) ([]byte, error) {
	limited := io.LimitReader(r.Body, MaxRequestBodyBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	_ = r.Body.Close()
	if len(body) > MaxRequestBodyBytes {
		return nil, fmt.Errorf("request body exceeds %d bytes", MaxRequestBodyBytes)
	}
	ReplaceBody(r, body)
	return body, nil
}

// ReplaceBody installs body as r's new Body/GetBody/ContentLength, so a
// handler that has already consumed and possibly modified the original
// body can hand a fresh copy to the next handler in the chain.
func ReplaceBody(r *http.Request, body []byte) {
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
}
