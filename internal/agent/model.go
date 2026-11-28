package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/tanrikuluozlem/burn/internal/plan"
)

// Local tools read the ledger; they do not reach the cluster.
const (
	toolListWorkloads = "list_workloads"
	toolGetEvidence   = "get_evidence"
)

// Model is the provider boundary; an adapter owns tokenization and decoding.
//
// Adapters must disable the SDK's automatic retries. Counting a retry in Usage
// is not enough: the step limit and deadline still see one call.
type Model interface {
	// EstimateInputTokens is advisory; reported usage is checked after the
	// call. A negative estimate ends the run.
	EstimateInputTokens(req Request) int

	// Complete honours ctx for cancellation and the deadline.
	Complete(ctx context.Context, req Request) (Response, error)
}

type Request struct {
	System          string
	Messages        []Message
	Tools           []ToolSpec
	MaxOutputTokens int
}

type Message struct {
	Role    string
	Content string
}

type ToolSpec struct {
	Name        string
	Description string
}

type Response struct {
	Decision Decision
	Usage    Usage
}

type Usage struct {
	InputTokens  int
	OutputTokens int
}

type ToolCall struct {
	Name string
	Args json.RawMessage
}

type DecisionKind string

const (
	DecisionTools   DecisionKind = "tools"
	DecisionPlan    DecisionKind = "plan"
	DecisionDecline DecisionKind = "decline"
)

// Decision is the only shape the loop accepts from a model. ToolCalls is a
// slice so parallel calls need no interface change; the loop runs them in order.
type Decision struct {
	Kind      DecisionKind
	ToolCalls []ToolCall
	Plan      *plan.Plan
	Reason    string
}

// Validate rejects malformed decisions. More than one payload is ambiguous
// even when one of them is well formed.
func (d Decision) Validate() error {
	payloads := 0
	if len(d.ToolCalls) > 0 {
		payloads++
	}
	if d.Plan != nil {
		payloads++
	}
	if d.Reason != "" {
		payloads++
	}
	if payloads > 1 {
		return fmt.Errorf("decision carries %d payloads, want one", payloads)
	}

	switch d.Kind {
	case DecisionTools:
		if len(d.ToolCalls) == 0 {
			return fmt.Errorf("decision %q has no tool calls", d.Kind)
		}
		for _, c := range d.ToolCalls {
			if !knownTool(c.Name) {
				return fmt.Errorf("unknown tool %q", c.Name)
			}
		}
	case DecisionPlan:
		if d.Plan == nil {
			return fmt.Errorf("decision %q has no plan", d.Kind)
		}
	case DecisionDecline:
		if d.Reason == "" {
			return fmt.Errorf("decision %q has no reason", d.Kind)
		}
	default:
		return fmt.Errorf("unknown decision %q", d.Kind)
	}
	return nil
}

func knownTool(name string) bool {
	switch name {
	case toolAnalyze, toolSpot, toolReconcile, toolListWorkloads, toolGetEvidence:
		return true
	}
	return false
}

func toolSpecs() []ToolSpec {
	return []ToolSpec{
		{toolAnalyze, "Collect cluster cost, savings and workload evidence."},
		{toolSpot, "Collect spot readiness evidence."},
		{toolReconcile, "Collect billing reconciliation evidence."},
		{toolListWorkloads, "Page through workloads already in the ledger."},
		{toolGetEvidence, "Read one field path of one evidence record."},
	}
}
