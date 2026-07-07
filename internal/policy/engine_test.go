package policy_test

import (
	"context"
	"testing"

	"github.com/conduit-oss/conduit/internal/policy"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestEngine_DefaultAllowWithNoRules(t *testing.T) {
	e := policy.New(zerolog.Nop())
	decision := e.Evaluate(context.Background(), policy.EvalInput{ToolName: "github/create_issue"})
	require.Equal(t, policy.ActionAllow, decision.Action)
	require.Equal(t, "__default__", decision.RuleName)
}

func TestEngine_DenyByGlob(t *testing.T) {
	e := policy.New(zerolog.Nop())
	e.LoadRules([]policy.Rule{
		{Name: "block-delete", Match: policy.MatchSpec{ToolName: "github/delete_*"}, Action: policy.ActionDeny, Message: "blocked"},
	})

	decision := e.Evaluate(context.Background(), policy.EvalInput{ToolName: "github/delete_repo"})
	require.Equal(t, policy.ActionDeny, decision.Action)
	require.Equal(t, "block-delete", decision.RuleName)
	require.Equal(t, "blocked", decision.Message)

	allowed := e.Evaluate(context.Background(), policy.EvalInput{ToolName: "github/create_issue"})
	require.Equal(t, policy.ActionAllow, allowed.Action)
}

func TestEngine_PriorityOrderingLowerFirst(t *testing.T) {
	e := policy.New(zerolog.Nop())
	e.LoadRules([]policy.Rule{
		{Name: "catch-all", Priority: 9999, Match: policy.MatchSpec{ToolName: "*"}, Action: policy.ActionAllow},
		{Name: "specific-deny", Priority: 10, Match: policy.MatchSpec{ToolName: "github/delete_repo"}, Action: policy.ActionDeny},
	})

	decision := e.Evaluate(context.Background(), policy.EvalInput{ToolName: "github/delete_repo"})
	require.Equal(t, "specific-deny", decision.RuleName)
}

func TestEngine_ExceptSkipsRule(t *testing.T) {
	e := policy.New(zerolog.Nop())
	e.LoadRules([]policy.Rule{
		{
			Name:    "production-readonly",
			Match:   policy.MatchSpec{ToolName: "*", AgentTag: "env:production"},
			Action:  policy.ActionDeny,
			Message: "writes blocked in production",
			Except:  &policy.ExceptSpec{ToolName: "*.read_*"},
		},
	})

	blocked := e.Evaluate(context.Background(), policy.EvalInput{
		ToolName:  "github/delete_repo",
		AgentTags: map[string]string{"env": "production"},
	})
	require.Equal(t, policy.ActionDeny, blocked.Action)

	allowed := e.Evaluate(context.Background(), policy.EvalInput{
		ToolName:  "github.read_repo",
		AgentTags: map[string]string{"env": "production"},
	})
	require.Equal(t, policy.ActionAllow, allowed.Action)
}

func TestEngine_TenantScopedRule(t *testing.T) {
	e := policy.New(zerolog.Nop())
	e.LoadRules([]policy.Rule{
		{Name: "acme-only", Match: policy.MatchSpec{TenantID: "acme", ToolName: "*"}, Action: policy.ActionDeny},
	})

	require.Equal(t, policy.ActionDeny, e.Evaluate(context.Background(), policy.EvalInput{TenantID: "acme", ToolName: "x"}).Action)
	require.Equal(t, policy.ActionAllow, e.Evaluate(context.Background(), policy.EvalInput{TenantID: "globex", ToolName: "x"}).Action)
}

func TestEngine_RateLimitDecisionCarriesSpec(t *testing.T) {
	e := policy.New(zerolog.Nop())
	e.LoadRules([]policy.Rule{
		{
			Name:      "throttle-reports",
			Match:     policy.MatchSpec{ToolName: "salesforce/generate_report"},
			Action:    policy.ActionRateLimit,
			RateLimit: &policy.RateLimitSpec{Requests: 5, Window: "1h"},
		},
	})

	decision := e.Evaluate(context.Background(), policy.EvalInput{ToolName: "salesforce/generate_report"})
	require.Equal(t, policy.ActionRateLimit, decision.Action)
	require.NotNil(t, decision.RateLimit)
	require.Equal(t, 5, decision.RateLimit.Requests)
}
