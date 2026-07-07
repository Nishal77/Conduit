// Package policy implements Conduit's YAML allow/deny rule engine
// (spec/15-policy.md): a synchronous, in-process decision made before a
// tool call is forwarded, evaluated in under 0.1ms with no I/O in the path.
package policy

// PolicyFile is the root of the YAML policy document.
type PolicyFile struct {
	Version string `yaml:"version"`
	Rules   []Rule `yaml:"rules"`
}

// Rule is a single policy rule: if Match (and not Except) applies to a
// request, Action is the decision.
type Rule struct {
	Name        string         `yaml:"name"`
	Description string         `yaml:"description"`
	Priority    int            `yaml:"priority"`
	Match       MatchSpec      `yaml:"match"`
	Action      Action         `yaml:"action"`
	Message     string         `yaml:"message"`
	RateLimit   *RateLimitSpec `yaml:"rate_limit"`
	Except      *ExceptSpec    `yaml:"except"`
}

// MatchSpec defines the conditions for a rule to apply. Every set field
// must match; an empty field matches anything for that dimension.
type MatchSpec struct {
	TenantID   string `yaml:"tenant_id"`
	ToolName   string `yaml:"tool_name"`
	ServerName string `yaml:"server_name"`
	AgentID    string `yaml:"agent_id"`
	AgentTag   string `yaml:"agent_tag"`
}

// ExceptSpec defines exclusions from a rule: if any set field matches, the
// rule is skipped even though Match applied.
type ExceptSpec struct {
	ToolName string `yaml:"tool_name"`
	AgentID  string `yaml:"agent_id"`
}

// RateLimitSpec defines a per-rule rate limit, used when Action is
// ActionRateLimit.
type RateLimitSpec struct {
	Requests int    `yaml:"requests"`
	Window   string `yaml:"window"` // "1m", "5m", "1h", "24h"
}

// Action is the policy decision a matched rule produces.
type Action string

const (
	ActionAllow     Action = "allow"
	ActionDeny      Action = "deny"
	ActionRateLimit Action = "rate_limit"
	ActionLog       Action = "log"
)

// Decision is the result of evaluating a request against the rule set.
type Decision struct {
	Action    Action
	RuleName  string
	Message   string
	RateLimit *RateLimitSpec // non-nil when Action == ActionRateLimit
}
