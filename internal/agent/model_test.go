package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tanrikuluozlem/burn/internal/plan"
)

func TestDecisionValidate(t *testing.T) {
	call := ToolCall{Name: toolAnalyze, Args: json.RawMessage(`{}`)}

	tests := []struct {
		name     string
		decision Decision
		wantErr  string
	}{
		{"tools", Decision{Kind: DecisionTools, ToolCalls: []ToolCall{call}}, ""},
		{"plan", Decision{Kind: DecisionPlan, Plan: &plan.Plan{}}, ""},
		{"decline", Decision{Kind: DecisionDecline, Reason: "no usable evidence"}, ""},

		{"unknown kind", Decision{Kind: "think"}, "unknown decision"},
		{"empty kind", Decision{}, "unknown decision"},
		{"tools without calls", Decision{Kind: DecisionTools}, "no tool calls"},
		{"plan without plan", Decision{Kind: DecisionPlan}, "no plan"},
		{"decline without reason", Decision{Kind: DecisionDecline}, "no reason"},
		{
			"tool not on the allowlist",
			Decision{Kind: DecisionTools, ToolCalls: []ToolCall{{Name: "kubectl"}}},
			"unknown tool",
		},
		{
			"two payloads",
			Decision{Kind: DecisionPlan, Plan: &plan.Plan{}, ToolCalls: []ToolCall{call}},
			"carries 2 payloads",
		},
	}

	for _, tt := range tests {
		err := tt.decision.Validate()
		switch {
		case tt.wantErr == "" && err != nil:
			t.Errorf("%s: unexpected error %v", tt.name, err)
		case tt.wantErr != "" && err == nil:
			t.Errorf("%s: expected an error naming %q", tt.name, tt.wantErr)
		case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
			t.Errorf("%s: error %q does not name %q", tt.name, err, tt.wantErr)
		}
	}
}

func TestDecisionAcceptsSeveralToolCalls(t *testing.T) {
	d := Decision{Kind: DecisionTools, ToolCalls: []ToolCall{
		{Name: toolAnalyze}, {Name: toolSpot}, {Name: toolListWorkloads},
	}}
	if err := d.Validate(); err != nil {
		t.Errorf("Validate() error = %v", err)
	}
}

func TestToolSpecsCoverTheAllowlist(t *testing.T) {
	for _, s := range toolSpecs() {
		if !knownTool(s.Name) {
			t.Errorf("advertised tool %q is not on the allowlist", s.Name)
		}
	}
	if !knownTool(toolReconcile) || knownTool(toolWorkloads) {
		t.Error("allowlist should hold the three cluster tools, not the evidence name")
	}
}
