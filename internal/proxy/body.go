package proxy

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
)

// maxRequestBodyBytes caps how much of an incoming request body Conduit
// will buffer in memory to run plugin.Before hooks. This only bounds the
// plugin inspection path (readAndReplaceBody); it is not a limit on
// streamed SSE traffic, which is never buffered (see sse.go).
const maxRequestBodyBytes = 10 * 1024 * 1024 // 10MB

// readAndReplaceBody reads r.Body fully (up to maxRequestBodyBytes), then
// replaces it with a fresh reader over the same bytes so downstream
// handlers can still read it. http.Request.Body is a single-use
// io.ReadCloser; any handler that needs to inspect the body (like
// PluginBeforeMiddleware) without consuming it for everyone downstream must
// do this dance.
func readAndReplaceBody(r *http.Request) ([]byte, error) {
	limited := io.LimitReader(r.Body, maxRequestBodyBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	_ = r.Body.Close()
	if len(body) > maxRequestBodyBytes {
		return nil, fmt.Errorf("request body exceeds %d bytes", maxRequestBodyBytes)
	}
	replaceBody(r, body)
	return body, nil
}

// replaceBody installs body as r's new Body/GetBody/ContentLength, so a
// handler that has already consumed and possibly modified the original
// body can hand a fresh copy to the next handler in the chain.
func replaceBody(r *http.Request, body []byte) {
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
}
