// Package builtin implements the five plugins spec/14-plugins.md §4
// ships with Conduit, each a plain ConduitPlugin — no different from a
// plugin a third party could write, just registered by default.
package builtin

import (
	"context"
	"encoding/json"
	"regexp"

	"github.com/conduit-oss/conduit/internal/mcp"
	"github.com/conduit-oss/conduit/internal/plugin"
)

// piiPattern is one PII detector: a compiled regex and the placeholder it's
// replaced with.
type piiPattern struct {
	name    string
	pattern *regexp.Regexp
	replace string
}

// piiPatterns are compiled once at package init, not per-call — spec/14's
// design goal is a Before hook cheap enough to run on every tool call.
var piiPatterns = []piiPattern{
	{"email", regexp.MustCompile(`\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b`), "[EMAIL]"},
	{"phone", regexp.MustCompile(`\b(?:\+?1[-.\s]?)?\(?\d{3}\)?[-.\s]?\d{3}[-.\s]?\d{4}\b`), "[PHONE]"},
	{"ssn", regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`), "[SSN]"},
	{"cc", regexp.MustCompile(`\b(?:\d[ -]?){13,16}\b`), "[CARD]"},
	{"apikey", regexp.MustCompile(`(?i)\b(api[_-]?key|token|secret|password)\s*[:=]\s*\S+`), "[SECRET]"},
}

// PIIRedactor detects and redacts common PII patterns (email, phone, SSN,
// card numbers, secret-shaped key=value pairs) in tools/call arguments
// before they reach the upstream server or the audit log.
type PIIRedactor struct{}

// NewPIIRedactor returns a ready-to-register PIIRedactor.
func NewPIIRedactor() *PIIRedactor { return &PIIRedactor{} }

func (p *PIIRedactor) Name() string    { return "pii-redactor" }
func (p *PIIRedactor) Version() string { return "1.0.0" }

// Before redacts PII found in a tools/call request's arguments. Non-call
// messages (tools/list, etc.) pass through unchanged — there are no
// "arguments" to redact.
func (p *PIIRedactor) Before(_ context.Context, req *mcp.Message) (*mcp.Message, error) {
	if req.Method != "tools/call" {
		return req, nil
	}

	var params mcp.ToolCallParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return req, nil
	}

	redacted, changed := redactPII(string(params.Arguments))
	if !changed {
		return req, nil
	}
	if !json.Valid([]byte(redacted)) {
		// The apikey pattern's \S+ is greedy enough that, on compact
		// (no-space) JSON, it can consume through a following comma or
		// brace and corrupt the structure — safer to skip redaction than
		// forward an unparseable payload upstream.
		return req, nil
	}

	params.Arguments = json.RawMessage(redacted)
	newParams, err := json.Marshal(params)
	if err != nil {
		return req, nil
	}

	out := *req
	out.Params = newParams
	return &out, nil
}

// After is a no-op: PII redaction only protects outbound arguments, not
// upstream responses (spec/14-plugins.md §4 scopes it to request_args).
func (p *PIIRedactor) After(_ context.Context, _, resp *mcp.Message) (*mcp.Message, error) {
	return resp, nil
}

func (p *PIIRedactor) Shutdown(context.Context) error { return nil }

// redactPII applies every pattern to s and reports whether anything changed.
func redactPII(s string) (result string, changed bool) {
	result = s
	for _, p := range piiPatterns {
		if p.pattern.MatchString(result) {
			result = p.pattern.ReplaceAllString(result, p.replace)
			changed = true
		}
	}
	return result, changed
}

var _ plugin.ConduitPlugin = (*PIIRedactor)(nil)
