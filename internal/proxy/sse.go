package proxy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/conduit-oss/conduit/internal/mcp"
	"github.com/conduit-oss/conduit/internal/plugin"
	"github.com/rs/zerolog/log"
)

// maxSSELineBytes bounds a single SSE line, protecting the proxy against an
// upstream that never sends a newline (accidental or malicious).
const maxSSELineBytes = 10 * 1024 * 1024 // 10MB

// sseScanBufferBytes is bufio.Scanner's initial buffer size; it grows up to
// maxSSELineBytes as needed for longer lines.
const sseScanBufferBytes = 64 * 1024

// SSEProxy streams a Server-Sent Events response from an upstream MCP
// server back to the agent, line by line, without ever buffering a full
// event in memory — critical for keeping proxy overhead low on long-lived
// tool-call streams (spec/02-proxy.md §3).
type SSEProxy struct {
	plugins *plugin.Registry
}

// Forward pipes upstreamResp's SSE body to w, running plugin.After hooks on
// any line that is a JSON-RPC response to callReq (i.e. the tools/call
// result). tenantID scopes which plugins' After hooks apply — see
// plugin.Registry.ForTenant.
//
// callReq may be nil (e.g. the request body wasn't a well-formed MCP
// message, or wasn't a tools/call): in that case every line passes through
// untouched, since there's nothing to match a response against.
func (s *SSEProxy) Forward(
	ctx context.Context,
	w http.ResponseWriter,
	upstreamResp *http.Response,
	callReq *mcp.Message,
	tenantID string,
) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return errors.New("response writer does not support flushing")
	}

	ActiveConnections.WithLabelValues(tenantID).Inc()
	defer ActiveConnections.WithLabelValues(tenantID).Dec()

	copyHeaders(w.Header(), upstreamResp.Header)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	if upstreamResp.ProtoAtLeast(1, 1) {
		w.Header().Set("Connection", "keep-alive")
	}
	w.WriteHeader(upstreamResp.StatusCode)
	flusher.Flush()

	scanner := bufio.NewScanner(upstreamResp.Body)
	scanner.Buffer(make([]byte, sseScanBufferBytes), maxSSELineBytes)

	isToolCall := callReq != nil && callReq.Method == "tools/call"

	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}

		line := scanner.Text()
		if isToolCall {
			line = s.processLine(ctx, line, callReq, tenantID)
		}

		if _, err := fmt.Fprintf(w, "%s\n", line); err != nil {
			return fmt.Errorf("write sse line: %w", err)
		}
		flusher.Flush()
	}

	if err := scanner.Err(); err != nil {
		log.Warn().Err(err).Msg("sse upstream closed unexpectedly")
		return nil // agent will reconnect; this is not a proxy-side failure
	}
	return nil
}

// processLine runs plugin.After against a "data: {...}" line that parses as
// a JSON-RPC response, returning the (possibly modified) line. Every other
// line — event:, id:, retry:, comments, blank separators, or a data: line
// that isn't valid JSON-RPC — passes through unchanged.
func (s *SSEProxy) processLine(ctx context.Context, line string, callReq *mcp.Message, tenantID string) (result string) {
	const dataPrefix = "data: "
	if !strings.HasPrefix(line, dataPrefix) {
		return line
	}

	payload := []byte(strings.TrimPrefix(line, dataPrefix))
	respMsg, err := mcp.ParseMessage(payload)
	if err != nil || !respMsg.IsResponse() {
		return line
	}

	result = line
	defer func() {
		// A plugin panicking mid-stream must not take down the whole SSE
		// connection — recover, log, and fall back to the original line.
		if r := recover(); r != nil {
			log.Error().Interface("panic", r).Msg("plugin After hook panicked, passing through original response")
			result = line
		}
	}()

	modified := s.plugins.RunAfter(ctx, tenantID, callReq, respMsg)
	if modified == nil {
		return line
	}
	out, err := json.Marshal(modified)
	if err != nil {
		return line
	}
	return dataPrefix + string(out)
}
