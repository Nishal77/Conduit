# Spec 15 — Policy Engine

> Phase: P6 | Files: `internal/policy/engine.go`, `internal/policy/loader.go`, `internal/policy/types.go`

---

## 1. Policy DSL

Policy rules are defined in YAML. The file is hot-reloaded every 10 seconds (or on file change via fsnotify).

### Full YAML Schema

```yaml
# /etc/conduit/policies.yaml
version: "1"

rules:
  - name: string              # required, unique, used in audit log
    description: string       # optional, for documentation
    priority: int             # optional, default 100 (lower = evaluated first)
    match:
      tenant_id: string       # exact UUID match; omit for all tenants
      tool_name: string       # supports "*" glob: "github/*", "*/delete_*", "*"
      server_name: string     # exact match; omit for all servers
      agent_id: string        # exact match; omit for all agents
      agent_tag: string       # matches "key:value" in agent metadata
    action: allow | deny | rate_limit | log
    message: string           # returned in 403 body when action=deny
    rate_limit:               # only used when action=rate_limit
      requests: int
      window: string          # "1m", "5m", "1h", "24h"
    except:                   # excludes from this rule if any match
      tool_name: string
      agent_id: string
```

### Example Rules

```yaml
version: "1"
rules:
  - name: block-destructive-github-tools
    priority: 10
    match:
      tool_name: "github/delete_*"
    action: deny
    message: "Destructive GitHub operations are blocked by policy"

  - name: restrict-salesforce-reports
    priority: 20
    match:
      tool_name: "salesforce/generate_report"
      tenant_id: "a1b2c3d4-..."
    action: rate_limit
    rate_limit:
      requests: 5
      window: 1h

  - name: production-env-readonly
    priority: 30
    match:
      tool_name: "*"
      agent_tag: "env:production"
    action: deny
    except:
      tool_name: "*.read_*"
    message: "Write operations are blocked in production environment"

  - name: allow-all
    priority: 9999
    match:
      tool_name: "*"
    action: allow
```

---

## 2. Types — `internal/policy/types.go`

```go
package policy

// PolicyFile is the root of the YAML policy document.
type PolicyFile struct {
    Version string `yaml:"version"`
    Rules   []Rule `yaml:"rules"`
}

// Rule is a single policy rule.
type Rule struct {
    Name        string      `yaml:"name"`
    Description string      `yaml:"description"`
    Priority    int         `yaml:"priority"`
    Match       MatchSpec   `yaml:"match"`
    Action      Action      `yaml:"action"`
    Message     string      `yaml:"message"`
    RateLimit   *RateLimitSpec `yaml:"rate_limit"`
    Except      *ExceptSpec `yaml:"except"`
}

// MatchSpec defines the conditions for a rule to apply.
type MatchSpec struct {
    TenantID   string `yaml:"tenant_id"`
    ToolName   string `yaml:"tool_name"`
    ServerName string `yaml:"server_name"`
    AgentID    string `yaml:"agent_id"`
    AgentTag   string `yaml:"agent_tag"`
}

// ExceptSpec defines exclusions from a rule.
type ExceptSpec struct {
    ToolName string `yaml:"tool_name"`
    AgentID  string `yaml:"agent_id"`
}

// RateLimitSpec defines a per-rule rate limit.
type RateLimitSpec struct {
    Requests int    `yaml:"requests"`
    Window   string `yaml:"window"`  // "1m", "5m", "1h", "24h"
}

// Action is the policy decision.
type Action string

const (
    ActionAllow     Action = "allow"
    ActionDeny      Action = "deny"
    ActionRateLimit Action = "rate_limit"
    ActionLog       Action = "log"
)

// Decision is the result of policy evaluation.
type Decision struct {
    Action   Action
    RuleName string
    Message  string
    RateLimit *RateLimitSpec  // non-nil when Action == ActionRateLimit
}
```

---

## 3. Policy Engine — `internal/policy/engine.go`

```go
package policy

import (
    "context"
    "path/filepath"
    "sort"
    "strings"
    "sync"
    "sync/atomic"
)

// Engine evaluates policy rules against a request context.
// The rule set is stored as an atomic value for lock-free reads.
type Engine struct {
    rules atomic.Value  // stores []Rule, sorted by priority
    log   zerolog.Logger
}

func New(log zerolog.Logger) *Engine

// LoadRules compiles and stores a new set of rules.
// Called at startup and on hot-reload.
func (e *Engine) LoadRules(rules []Rule) {
    sorted := make([]Rule, len(rules))
    copy(sorted, rules)
    sort.Slice(sorted, func(i, j int) bool {
        pi, pj := sorted[i].Priority, sorted[j].Priority
        if pi == 0 { pi = 100 }
        if pj == 0 { pj = 100 }
        return pi < pj
    })
    e.rules.Store(sorted)
}

// Evaluate runs all rules against the given request context.
// Returns the first matching decision. If no rule matches: default allow.
//
// Algorithm:
//   1. Load current rules (atomic load, no lock)
//   2. For each rule in priority order:
//      a. Check match conditions (all must match):
//         - tenant_id: empty = match all, else exact UUID match
//         - tool_name: supports "*" prefix/suffix/both glob
//         - server_name: empty = match all, else exact
//         - agent_id: empty = match all, else exact
//         - agent_tag: empty = match all, else "key:value" match
//      b. Check except conditions (if any match, skip this rule)
//      c. Return Decision for this rule
//   3. No match: return Decision{Action: ActionAllow, RuleName: "__default__"}
//
// Performance: this MUST complete in <0.1ms
//   - No I/O in the evaluation path
//   - All patterns are pre-compiled globs, not regex
//   - Atomic rule set read = no lock contention
func (e *Engine) Evaluate(ctx context.Context, input EvalInput) Decision

// EvalInput is the input to policy evaluation.
type EvalInput struct {
    TenantID   string
    ToolName   string
    ServerName string
    AgentID    string
    AgentTags  map[string]string  // key → value
}
```

### Glob Matching Rules

```go
// matchGlob returns true if pattern matches value.
// Supported patterns:
//   "*"         — matches anything
//   "github/*"  — prefix glob
//   "*/delete_*" — both prefix and suffix glob
//   "github/create_issue" — exact match
func matchGlob(pattern, value string) bool {
    if pattern == "" || pattern == "*" {
        return true
    }
    // Use filepath.Match which supports * (not **)
    // Replace "*" with "**" for multi-segment matching if needed
    matched, _ := filepath.Match(pattern, value)
    return matched
}
```

---

## 4. Policy Loader — `internal/policy/loader.go`

```go
package policy

import (
    "context"
    "os"
    "time"

    "github.com/fsnotify/fsnotify"
    "gopkg.in/yaml.v3"
)

// Loader watches a policy file and reloads it when changed.
type Loader struct {
    filePath string
    engine   *Engine
    interval time.Duration
    log      zerolog.Logger
    watcher  *fsnotify.Watcher
}

func NewLoader(filePath string, engine *Engine, interval time.Duration, log zerolog.Logger) (*Loader, error)

// Start begins watching the policy file.
// Loads the file immediately, then watches for changes.
// Stops when ctx is cancelled.
func (l *Loader) Start(ctx context.Context) error {
    // 1. Load file immediately
    if err := l.load(); err != nil {
        return fmt.Errorf("initial policy load: %w", err)
    }
    // 2. Add fsnotify watch
    // 3. Start goroutine: on WRITE/RENAME/CREATE event for filePath, call l.load()
    // 4. Also set a periodic reload ticker (interval) as fallback
}

// load reads and parses the policy file, then calls engine.LoadRules.
func (l *Loader) load() error {
    data, err := os.ReadFile(l.filePath)
    if err != nil {
        return fmt.Errorf("read policy file: %w", err)
    }

    var pf PolicyFile
    if err := yaml.Unmarshal(data, &pf); err != nil {
        return fmt.Errorf("parse policy yaml: %w", err)
    }

    if err := validateRules(pf.Rules); err != nil {
        return fmt.Errorf("validate policy rules: %w", err)
    }

    l.engine.LoadRules(pf.Rules)
    l.log.Info().Int("count", len(pf.Rules)).Msg("policy rules loaded")
    return nil
}

// validateRules checks rule names are unique and actions are valid.
func validateRules(rules []Rule) error {
    seen := make(map[string]bool)
    for _, r := range rules {
        if r.Name == "" {
            return errors.New("rule missing name")
        }
        if seen[r.Name] {
            return fmt.Errorf("duplicate rule name: %q", r.Name)
        }
        seen[r.Name] = true
        if r.Action != ActionAllow && r.Action != ActionDeny &&
            r.Action != ActionRateLimit && r.Action != ActionLog {
            return fmt.Errorf("rule %q has invalid action: %q", r.Name, r.Action)
        }
        if r.Action == ActionRateLimit && r.RateLimit == nil {
            return fmt.Errorf("rule %q has action rate_limit but no rate_limit config", r.Name)
        }
    }
    return nil
}
```

---

## 5. Policy Middleware

```go
// In the proxy middleware chain (internal/proxy/middleware.go):
func PolicyMiddleware(engine *policy.Engine) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            // If no policy file configured: skip (default allow)
            if engine == nil {
                next.ServeHTTP(w, r)
                return
            }

            decision := engine.Evaluate(r.Context(), policy.EvalInput{
                TenantID:   proxy.TenantIDFromContext(r.Context()),
                ToolName:   proxy.ToolNameFromContext(r.Context()),
                ServerName: proxy.ServerNameFromContext(r.Context()),
                AgentID:    proxy.AgentIDFromContext(r.Context()),
            })

            // Store decision in context for audit log
            ctx := context.WithValue(r.Context(), ctxKeyPolicyAction, string(decision.Action))
            ctx = context.WithValue(ctx, ctxKeyPolicyRule, decision.RuleName)

            if decision.Action == policy.ActionDeny {
                writeError(w, r, http.StatusForbidden, decision.Message, "FORBIDDEN")
                return
            }

            next.ServeHTTP(w, r.WithContext(ctx))
        })
    }
}
```
