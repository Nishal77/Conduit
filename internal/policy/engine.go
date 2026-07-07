package policy

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/rs/zerolog"
)

// defaultPriority is used when a Rule doesn't set one explicitly. Lower
// priority values are evaluated first.
const defaultPriority = 100

// Engine evaluates policy rules against a request context. The rule set is
// stored as an atomic value so Evaluate never takes a lock on the hot
// path — LoadRules (called at startup and on every hot-reload) is the only
// writer.
type Engine struct {
	rules atomic.Value // stores []Rule, sorted by priority ascending
	log   zerolog.Logger
}

// New returns an Engine with no rules loaded — Evaluate defaults to allow
// until LoadRules is called, so a proxy started without a policy file
// configured behaves exactly as it did before Phase 6.
func New(log zerolog.Logger) *Engine {
	e := &Engine{log: log}
	e.rules.Store([]Rule{})
	return e
}

// LoadRules compiles and stores a new set of rules, sorted by priority.
// Called at startup and on every hot-reload; safe to call concurrently
// with Evaluate.
func (e *Engine) LoadRules(rules []Rule) {
	sorted := make([]Rule, len(rules))
	copy(sorted, rules)
	sort.SliceStable(sorted, func(i, j int) bool {
		pi, pj := sorted[i].Priority, sorted[j].Priority
		if pi == 0 {
			pi = defaultPriority
		}
		if pj == 0 {
			pj = defaultPriority
		}
		return pi < pj
	})
	e.rules.Store(sorted)
}

// EvalInput is the input to policy evaluation, gathered from the
// authenticated request context and the resolved tool call.
type EvalInput struct {
	TenantID   string
	ToolName   string
	ServerName string
	AgentID    string
	AgentTags  map[string]string // key -> value
}

// Evaluate runs every rule in priority order and returns the first matching
// decision, or a default allow if none match. This MUST stay allocation-light
// and I/O-free: it runs on every proxied tool call, budgeted at <0.1ms
// (spec/15-policy.md §3).
func (e *Engine) Evaluate(_ context.Context, input EvalInput) Decision {
	rules, _ := e.rules.Load().([]Rule)
	for _, rule := range rules {
		if !matchSpec(rule.Match, input) {
			continue
		}
		if rule.Except != nil && matchExcept(*rule.Except, input) {
			continue
		}
		return Decision{
			Action:    rule.Action,
			RuleName:  rule.Name,
			Message:   rule.Message,
			RateLimit: rule.RateLimit,
		}
	}
	return Decision{Action: ActionAllow, RuleName: "__default__"}
}

// matchSpec reports whether every set field in m matches input. An empty
// field matches anything for that dimension.
func matchSpec(m MatchSpec, input EvalInput) bool {
	if m.TenantID != "" && m.TenantID != input.TenantID {
		return false
	}
	if m.ToolName != "" && !matchGlob(m.ToolName, input.ToolName) {
		return false
	}
	if m.ServerName != "" && m.ServerName != input.ServerName {
		return false
	}
	if m.AgentID != "" && m.AgentID != input.AgentID {
		return false
	}
	if m.AgentTag != "" && !matchAgentTag(m.AgentTag, input.AgentTags) {
		return false
	}
	return true
}

// matchExcept reports whether any set field in e matches input — a single
// match is enough to exclude the request from the rule that owns this
// ExceptSpec.
func matchExcept(e ExceptSpec, input EvalInput) bool {
	if e.ToolName != "" && matchGlob(e.ToolName, input.ToolName) {
		return true
	}
	if e.AgentID != "" && e.AgentID == input.AgentID {
		return true
	}
	return false
}

// matchAgentTag checks a "key:value" spec against the request's agent tags.
func matchAgentTag(spec string, tags map[string]string) bool {
	key, value, ok := strings.Cut(spec, ":")
	if !ok {
		return false
	}
	return tags[key] == value
}

// matchGlob returns true if pattern matches value. Supported patterns:
//
//	""                    — matches anything (an unset match field)
//	"*"                   — matches anything
//	"github/*"            — prefix glob
//	"*/delete_*"          — prefix and suffix glob
//	"github/create_issue" — exact match
//
// filepath.Match's "*" never crosses a literal "/" in value, which is
// exactly the boundary tool names ("server/tool") need.
func matchGlob(pattern, value string) bool {
	if pattern == "" || pattern == "*" {
		return true
	}
	matched, _ := filepath.Match(pattern, value)
	return matched
}
