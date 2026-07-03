package proxy

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"strings"
)

// maxJSONResponseBytes caps how much of a non-SSE upstream response
// Conduit will buffer in memory. tools/list and similar JSON responses are
// small; anything approaching this limit is almost certainly a misbehaving
// upstream, not legitimate MCP traffic (large payloads belong in SSE
// content blocks, which stream instead of buffering — see sse.go).
const maxJSONResponseBytes = 10 * 1024 * 1024 // 10MB

// isSSE reports whether an upstream response is a Server-Sent Events
// stream, based on its Content-Type header per spec/01-mcp-protocol.md §7.
func isSSE(resp *http.Response) bool {
	return strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream")
}

// newBodyReader wraps body so it can be used as an http.Request's Body.
// Returns nil for an empty body so GET-style requests don't send a
// spurious empty body.
func newBodyReader(body []byte) io.Reader {
	if len(body) == 0 {
		return nil
	}
	return bytes.NewReader(body)
}

// readBodyLimited reads r fully, capped at maxJSONResponseBytes.
func readBodyLimited(r io.Reader) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, maxJSONResponseBytes+1))
}

// copyHeaders copies every header from src to dst except hop-by-hop
// headers, which must never be forwarded (RFC 7230 §6.1).
func copyHeaders(dst, src http.Header) {
	for key, values := range src {
		if isHopByHop(key) {
			continue
		}
		for _, v := range values {
			dst.Add(key, v)
		}
	}
}

func isHopByHop(header string) bool {
	for _, h := range hopByHopHeaders {
		if strings.EqualFold(h, header) {
			return true
		}
	}
	return false
}

// clientIP extracts the caller's IP from r.RemoteAddr, falling back to the
// raw value if it isn't a valid host:port pair (e.g. in unit tests).
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
