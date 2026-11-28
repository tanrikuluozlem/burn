package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/tanrikuluozlem/burn/internal/plan"
)

type fakeModel struct {
	estimate int
	steps    []func(req Request) (Response, error)
	reqs     []Request
	calls    int
}

func (m *fakeModel) EstimateInputTokens(Request) int { return m.estimate }

func (m *fakeModel) Complete(_ context.Context, req Request) (Response, error) {
	m.reqs = append(m.reqs, req)
	i := m.calls
	m.calls++
	if i < len(m.steps) {
		return m.steps[i](req)
	}
	return declines("script exhausted")(req)
}

type fakeTools struct {
	analyze   func(context.Context, AnalyzeInput) ([]plan.Evidence, error)
	spot      func(context.Context, SpotInput) ([]plan.Evidence, error)
	reconcile func(context.Context, ReconcileInput) ([]plan.Evidence, error)
	calls     int
}

func (f *fakeTools) Analyze(ctx context.Context, in AnalyzeInput) ([]plan.Evidence, error) {
	f.calls++
	if f.analyze == nil {
		return nil, errors.New("not configured")
	}
	return f.analyze(ctx, in)
}

func (f *fakeTools) SpotReadiness(ctx context.Context, in SpotInput) ([]plan.Evidence, error) {
	f.calls++
	if f.spot == nil {
		return nil, errors.New("not configured")
	}
	return f.spot(ctx, in)
}

func (f *fakeTools) Reconcile(ctx context.Context, in ReconcileInput) ([]plan.Evidence, error) {
	f.calls++
	if f.reconcile == nil {
		return nil, errors.New("not configured")
	}
	return f.reconcile(ctx, in)
}

type timeoutError struct{}

func (timeoutError) Error() string { return "i/o timeout" }
func (timeoutError) Timeout() bool { return true }

func callsTool(name, args string) func(Request) (Response, error) {
	return func(Request) (Response, error) {
		return Response{Decision: Decision{
			Kind:      DecisionTools,
			ToolCalls: []ToolCall{{Name: name, Args: json.RawMessage(args)}},
		}}, nil
	}
}

func declines(reason string) func(Request) (Response, error) {
	return func(Request) (Response, error) {
		return Response{Decision: Decision{Kind: DecisionDecline, Reason: reason}}, nil
	}
}

func submits(p *plan.Plan) func(Request) (Response, error) {
	return func(Request) (Response, error) {
		return Response{Decision: Decision{Kind: DecisionPlan, Plan: p}}, nil
	}
}

func testBudget() Budget {
	return Budget{Steps: 10, InputTokens: 1000, OutputTokens: 1000, WallClock: time.Minute, RetriesPerCall: 2}
}

func evidenceAndPlan(t *testing.T) (plan.Evidence, *plan.Plan) {
	t.Helper()
	rec := workloadRecord(t, overProvisionedPod("prod", "api-1", "Deployment", "api"))

	var doc workloadEvidence
	if err := json.Unmarshal(rec.Data, &doc); err != nil {
		t.Fatalf("decode evidence: %v", err)
	}
	w := doc.Workloads[0]
	if w.CurrentCPUMillicores == nil || w.CandidateCPUMillicores == nil || w.CandidateAllocationSavingsMonthly == nil {
		t.Fatalf("fixture produced no priced candidate: %+v", w)
	}

	ref := func(path string) plan.EvidenceRef {
		return plan.EvidenceRef{EvidenceID: rec.ID, Path: path}
	}
	p := &plan.Plan{
		SchemaVersion: plan.SchemaVersion,
		ID:            "run-1",
		CreatedAt:     time.Date(2026, 11, 28, 9, 0, 0, 0, time.UTC),
		Goal:          "reduce cost without reliability risk",
		Readiness:     plan.ReadinessInvestigation,
		Target:        plan.Target{Kind: "Deployment", Namespace: "prod", Name: "api"},
		Current: plan.Resources{
			CPUMillicores: &plan.Value{Amount: *w.CurrentCPUMillicores, Source: ref("workloads.0.current_cpu_millicores")},
		},
		Proposed: plan.Resources{
			CPUMillicores: &plan.Value{Amount: *w.CandidateCPUMillicores, Source: ref("workloads.0.candidate_cpu_millicores")},
		},
		ExpectedSavings: &plan.Value{
			Amount: *w.CandidateAllocationSavingsMonthly,
			Source: ref("workloads.0.candidate_allocation_savings_monthly"),
		},
		Confidence:               "medium",
		Blockers:                 w.Blockers,
		UnsupportedPrerequisites: doc.NotCollected,
	}
	return rec, p
}

func returning(rec plan.Evidence) func(context.Context, AnalyzeInput) ([]plan.Evidence, error) {
	return func(context.Context, AnalyzeInput) ([]plan.Evidence, error) {
		return []plan.Evidence{rec}, nil
	}
}

func TestRunProducesValidatedPlan(t *testing.T) {
	rec, p := evidenceAndPlan(t)
	tools := &fakeTools{analyze: returning(rec)}
	model := &fakeModel{steps: []func(Request) (Response, error){
		callsTool(toolAnalyze, `{}`),
		submits(p),
	}}

	res, err := NewRunner(model, tools, testBudget()).Run(context.Background(), "cut cost")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.Termination != TerminatedPlan {
		t.Fatalf("termination = %q, want %q", res.Termination, TerminatedPlan)
	}
	if res.Plan == nil {
		t.Fatal("a validated plan must be returned")
	}
	if len(res.Ledger) != 1 {
		t.Errorf("ledger holds %d records, want 1", len(res.Ledger))
	}
}

func TestRunRejectsFabricatedPlan(t *testing.T) {
	rec, p := evidenceAndPlan(t)
	p.ExpectedSavings.Amount = 9999

	tools := &fakeTools{analyze: returning(rec)}
	model := &fakeModel{steps: []func(Request) (Response, error){
		callsTool(toolAnalyze, `{}`),
		submits(p),
	}}

	res, _ := NewRunner(model, tools, testBudget()).Run(context.Background(), "cut cost")
	if res.Termination != TerminatedInvalidPlan {
		t.Fatalf("termination = %q, want %q", res.Termination, TerminatedInvalidPlan)
	}
	if res.Plan != nil {
		t.Error("an unvalidated plan must never be returned")
	}
}

func TestConfidenceDoesNotBypassValidation(t *testing.T) {
	rec, p := evidenceAndPlan(t)
	p.Confidence = "high"
	p.ExpectedSavings.Amount = 4242

	tools := &fakeTools{analyze: returning(rec)}
	model := &fakeModel{steps: []func(Request) (Response, error){
		callsTool(toolAnalyze, `{}`),
		submits(p),
	}}

	res, _ := NewRunner(model, tools, testBudget()).Run(context.Background(), "cut cost")
	if res.Plan != nil || res.Termination != TerminatedInvalidPlan {
		t.Errorf("termination = %q with plan %v, want the plan refused", res.Termination, res.Plan)
	}
}

func TestRunDeclines(t *testing.T) {
	model := &fakeModel{steps: []func(Request) (Response, error){declines("metrics are missing")}}

	res, err := NewRunner(model, &fakeTools{}, testBudget()).Run(context.Background(), "cut cost")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.Termination != TerminatedDeclined || res.Plan != nil {
		t.Errorf("termination = %q, plan = %v; want a clean decline", res.Termination, res.Plan)
	}
}

func TestRunStopsAtStepBudget(t *testing.T) {
	rec, _ := evidenceAndPlan(t)
	tools := &fakeTools{analyze: func(_ context.Context, in AnalyzeInput) ([]plan.Evidence, error) {
		return []plan.Evidence{rec}, nil
	}}

	var steps []func(Request) (Response, error)
	for i := range 20 {
		steps = append(steps, callsTool(toolAnalyze, fmt.Sprintf(`{"namespace":"ns-%d"}`, i)))
	}
	model := &fakeModel{steps: steps}

	b := testBudget()
	b.Steps = 5
	res, _ := NewRunner(model, tools, b).Run(context.Background(), "cut cost")

	if res.Termination != TerminatedStepBudget {
		t.Fatalf("termination = %q, want %q", res.Termination, TerminatedStepBudget)
	}
	if res.Spent.Steps > b.Steps {
		t.Errorf("spent %d steps, want at most %d", res.Spent.Steps, b.Steps)
	}
	if res.Spent.ModelCalls+res.Spent.ToolCalls > b.Steps {
		t.Errorf("model calls %d + tool calls %d exceed the step budget %d",
			res.Spent.ModelCalls, res.Spent.ToolCalls, b.Steps)
	}
}

func TestRunRefusesOversizedRequestBeforeCalling(t *testing.T) {
	model := &fakeModel{estimate: 10_000, steps: []func(Request) (Response, error){declines("unused")}}

	res, _ := NewRunner(model, &fakeTools{}, testBudget()).Run(context.Background(), "cut cost")
	if res.Termination != TerminatedTokenBudget {
		t.Fatalf("termination = %q, want %q", res.Termination, TerminatedTokenBudget)
	}
	if model.calls != 0 {
		t.Errorf("model called %d times, want none", model.calls)
	}
}

// Reported usage decides, not the estimate.
func TestRunRejectsOverBudgetResponseCarryingAPlan(t *testing.T) {
	rec, p := evidenceAndPlan(t)
	tools := &fakeTools{analyze: returning(rec)}
	model := &fakeModel{steps: []func(Request) (Response, error){
		callsTool(toolAnalyze, `{}`),
		func(Request) (Response, error) {
			return Response{
				Decision: Decision{Kind: DecisionPlan, Plan: p},
				Usage:    Usage{OutputTokens: 5000},
			}, nil
		},
	}}

	b := testBudget()
	b.OutputTokens = 100
	res, _ := NewRunner(model, tools, b).Run(context.Background(), "cut cost")

	if res.Termination != TerminatedTokenBudget {
		t.Fatalf("termination = %q, want %q", res.Termination, TerminatedTokenBudget)
	}
	if res.Plan != nil {
		t.Error("an over-budget response must not yield a plan")
	}
}

func TestRunEnforcesInputTokensAfterTheCall(t *testing.T) {
	model := &fakeModel{steps: []func(Request) (Response, error){
		func(Request) (Response, error) {
			return Response{
				Decision: Decision{Kind: DecisionDecline, Reason: "late"},
				Usage:    Usage{InputTokens: 5000},
			}, nil
		},
	}}

	b := testBudget()
	b.InputTokens = 100
	res, _ := NewRunner(model, &fakeTools{}, b).Run(context.Background(), "cut cost")

	if res.Termination != TerminatedTokenBudget {
		t.Errorf("termination = %q, want %q", res.Termination, TerminatedTokenBudget)
	}
}

func TestRequestCarriesRemainingOutputBudget(t *testing.T) {
	rec, _ := evidenceAndPlan(t)
	tools := &fakeTools{analyze: returning(rec)}
	model := &fakeModel{steps: []func(Request) (Response, error){
		func(Request) (Response, error) {
			return Response{
				Decision: Decision{Kind: DecisionTools, ToolCalls: []ToolCall{{Name: toolAnalyze, Args: json.RawMessage(`{}`)}}},
				Usage:    Usage{OutputTokens: 300},
			}, nil
		},
		declines("done"),
	}}

	b := testBudget()
	b.OutputTokens = 1000
	if _, err := NewRunner(model, tools, b).Run(context.Background(), "cut cost"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if len(model.reqs) < 2 {
		t.Fatalf("model called %d times, want 2", len(model.reqs))
	}
	if model.reqs[0].MaxOutputTokens != 1000 {
		t.Errorf("first request cap = %d, want 1000", model.reqs[0].MaxOutputTokens)
	}
	if model.reqs[1].MaxOutputTokens != 700 {
		t.Errorf("second request cap = %d, want the remaining 700", model.reqs[1].MaxOutputTokens)
	}
}

func TestRunStopsOnRepeatedToolCall(t *testing.T) {
	rec, _ := evidenceAndPlan(t)
	tools := &fakeTools{analyze: returning(rec)}
	model := &fakeModel{steps: []func(Request) (Response, error){
		callsTool(toolAnalyze, `{"period":"7d"}`),
		callsTool(toolAnalyze, `{"period":"7d"}`),
	}}

	res, _ := NewRunner(model, tools, testBudget()).Run(context.Background(), "cut cost")
	if res.Termination != TerminatedNoProgress {
		t.Fatalf("termination = %q, want %q", res.Termination, TerminatedNoProgress)
	}
	if tools.calls != 1 {
		t.Errorf("tool ran %d times, want the repeat caught before execution", tools.calls)
	}
}

// An omitted field and its zero value are the same call.
func TestEquivalentArgumentsShareOneProgressKey(t *testing.T) {
	rec, _ := evidenceAndPlan(t)
	tools := &fakeTools{analyze: returning(rec)}
	model := &fakeModel{steps: []func(Request) (Response, error){
		callsTool(toolAnalyze, `{"period":"7d"}`),
		callsTool(toolAnalyze, `{"period":"7d","namespace":""}`),
	}}

	res, _ := NewRunner(model, tools, testBudget()).Run(context.Background(), "cut cost")
	if res.Termination != TerminatedNoProgress {
		t.Errorf("termination = %q, want equivalent arguments to count as a repeat", res.Termination)
	}
}

func TestRunRetriesTransientToolFailure(t *testing.T) {
	rec, _ := evidenceAndPlan(t)
	attempts := 0
	tools := &fakeTools{analyze: func(context.Context, AnalyzeInput) ([]plan.Evidence, error) {
		attempts++
		if attempts == 1 {
			return nil, timeoutError{}
		}
		return []plan.Evidence{rec}, nil
	}}
	model := &fakeModel{steps: []func(Request) (Response, error){
		callsTool(toolAnalyze, `{}`),
		declines("done"),
	}}

	res, _ := NewRunner(model, tools, testBudget()).Run(context.Background(), "cut cost")
	if attempts != 2 {
		t.Errorf("tool attempted %d times, want one retry", attempts)
	}
	if res.Spent.Retries != 1 {
		t.Errorf("retries counted = %d, want 1", res.Spent.Retries)
	}
	if len(res.Ledger) != 1 {
		t.Errorf("ledger holds %d records, want the successful retry recorded", len(res.Ledger))
	}
}

func TestRunDoesNotRetryUnavailableOrUnknownErrors(t *testing.T) {
	tests := map[string]error{
		"unavailable": fmt.Errorf("%w: cur not set", ErrUnavailable),
		"unknown":     errors.New("something broke"),
	}

	for name, toolErr := range tests {
		attempts := 0
		tools := &fakeTools{analyze: func(context.Context, AnalyzeInput) ([]plan.Evidence, error) {
			attempts++
			return nil, toolErr
		}}
		model := &fakeModel{steps: []func(Request) (Response, error){
			callsTool(toolAnalyze, `{}`),
			declines("done"),
		}}

		res, _ := NewRunner(model, tools, testBudget()).Run(context.Background(), "cut cost")
		if attempts != 1 {
			t.Errorf("%s: tool attempted %d times, want no retry", name, attempts)
		}
		if res.Spent.Retries != 0 {
			t.Errorf("%s: retries = %d, want 0", name, res.Spent.Retries)
		}
	}
}

func TestRetriesCountAgainstTheStepBudget(t *testing.T) {
	tools := &fakeTools{analyze: func(context.Context, AnalyzeInput) ([]plan.Evidence, error) {
		return nil, timeoutError{}
	}}
	model := &fakeModel{steps: []func(Request) (Response, error){callsTool(toolAnalyze, `{}`)}}

	b := testBudget()
	b.Steps = 3
	b.RetriesPerCall = 10
	res, _ := NewRunner(model, tools, b).Run(context.Background(), "cut cost")

	if res.Spent.Steps > b.Steps {
		t.Errorf("spent %d steps, want at most %d", res.Spent.Steps, b.Steps)
	}
	if res.Spent.Retries >= b.RetriesPerCall {
		t.Errorf("retries = %d; the step budget should have stopped them first", res.Spent.Retries)
	}
}

func TestToolErrorsStayOutOfTheLedger(t *testing.T) {
	tools := &fakeTools{analyze: func(context.Context, AnalyzeInput) ([]plan.Evidence, error) {
		return nil, errors.New("collector unreachable")
	}}
	model := &fakeModel{steps: []func(Request) (Response, error){
		callsTool(toolAnalyze, `{}`),
		declines("no evidence"),
	}}

	res, _ := NewRunner(model, tools, testBudget()).Run(context.Background(), "cut cost")
	if len(res.Ledger) != 0 {
		t.Errorf("ledger holds %d records, want none", len(res.Ledger))
	}
	var logged bool
	for _, s := range res.Steps {
		if s.Error != "" {
			logged = true
		}
	}
	if !logged {
		t.Error("the failure should appear in the step log")
	}
}

func TestRunRejectsMalformedDecisionAndContinues(t *testing.T) {
	model := &fakeModel{steps: []func(Request) (Response, error){
		func(Request) (Response, error) { return Response{Decision: Decision{Kind: "ponder"}}, nil },
		declines("recovered"),
	}}

	res, err := NewRunner(model, &fakeTools{}, testBudget()).Run(context.Background(), "cut cost")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.Termination != TerminatedDeclined {
		t.Errorf("termination = %q, want the loop to continue past a bad decision", res.Termination)
	}
	if model.calls != 2 {
		t.Errorf("model called %d times, want the rejection fed back", model.calls)
	}
}

func TestRunRejectsUnknownToolBeforeExecuting(t *testing.T) {
	tools := &fakeTools{}
	model := &fakeModel{steps: []func(Request) (Response, error){
		callsTool("kubectl", `{}`),
		declines("done"),
	}}

	if _, err := NewRunner(model, tools, testBudget()).Run(context.Background(), "cut cost"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if tools.calls != 0 {
		t.Errorf("tool ran %d times, want none for a tool off the allowlist", tools.calls)
	}
}

func TestRunStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	model := &fakeModel{steps: []func(Request) (Response, error){declines("unused")}}
	res, err := NewRunner(model, &fakeTools{}, testBudget()).Run(ctx, "cut cost")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.Termination != TerminatedCancelled {
		t.Errorf("termination = %q, want %q", res.Termination, TerminatedCancelled)
	}
	if model.calls != 0 {
		t.Errorf("model called %d times after cancellation", model.calls)
	}
}

func TestCancellationBeforeAcceptingPlan(t *testing.T) {
	rec, p := evidenceAndPlan(t)
	ctx, cancel := context.WithCancel(context.Background())

	tools := &fakeTools{analyze: returning(rec)}
	model := &fakeModel{steps: []func(Request) (Response, error){
		callsTool(toolAnalyze, `{}`),
		func(Request) (Response, error) {
			cancel()
			return Response{Decision: Decision{Kind: DecisionPlan, Plan: p}}, nil
		},
	}}

	res, _ := NewRunner(model, tools, testBudget()).Run(ctx, "cut cost")
	if res.Termination != TerminatedCancelled {
		t.Fatalf("termination = %q, want %q", res.Termination, TerminatedCancelled)
	}
	if res.Plan != nil {
		t.Error("a plan must not be accepted after cancellation")
	}
}

func TestRunStopsAtWallClock(t *testing.T) {
	model := &fakeModel{steps: []func(Request) (Response, error){declines("unused")}}

	b := testBudget()
	b.WallClock = time.Nanosecond
	res, _ := NewRunner(model, &fakeTools{}, b).Run(context.Background(), "cut cost")

	if res.Termination != TerminatedWallClock {
		t.Errorf("termination = %q, want %q", res.Termination, TerminatedWallClock)
	}
}

func TestLocalRetrievalCountsAgainstTheBudget(t *testing.T) {
	rec, _ := evidenceAndPlan(t)
	tools := &fakeTools{analyze: returning(rec)}
	model := &fakeModel{steps: []func(Request) (Response, error){
		callsTool(toolAnalyze, `{}`),
		callsTool(toolListWorkloads, `{}`),
		callsTool(toolGetEvidence, fmt.Sprintf(`{"evidence_id":%q,"path":"workloads.0.name"}`, rec.ID)),
		declines("done"),
	}}

	res, _ := NewRunner(model, tools, testBudget()).Run(context.Background(), "cut cost")
	if res.Spent.ToolCalls != 3 {
		t.Errorf("tool calls = %d, want the two local reads counted", res.Spent.ToolCalls)
	}
	if len(res.Ledger) != 1 {
		t.Errorf("ledger holds %d records, want local reads to add none", len(res.Ledger))
	}
}

func TestRunRejectsBadToolArgumentsWithoutRetrying(t *testing.T) {
	tools := &fakeTools{}
	model := &fakeModel{steps: []func(Request) (Response, error){
		callsTool(toolAnalyze, `{"nonsense":1}`),
		declines("done"),
	}}

	res, _ := NewRunner(model, tools, testBudget()).Run(context.Background(), "cut cost")
	if tools.calls != 0 {
		t.Errorf("tool ran %d times, want arguments rejected first", tools.calls)
	}
	if res.Spent.Retries != 0 {
		t.Errorf("retries = %d, want none for a permanent argument error", res.Spent.Retries)
	}
}

func TestRunRejectsInvalidUsage(t *testing.T) {
	rec, p := evidenceAndPlan(t)

	tests := map[string]Usage{
		"negative output":  {OutputTokens: -1},
		"negative input":   {InputTokens: -1},
		"both negative":    {InputTokens: -5, OutputTokens: -5},
		"output overflows": {OutputTokens: math.MaxInt},
		"input overflows":  {InputTokens: math.MaxInt},
	}

	for name, usage := range tests {
		tools := &fakeTools{analyze: returning(rec)}
		model := &fakeModel{steps: []func(Request) (Response, error){
			callsTool(toolAnalyze, `{}`),
			func(Request) (Response, error) {
				// The response carries a plan that would otherwise validate.
				return Response{Decision: Decision{Kind: DecisionPlan, Plan: p}, Usage: usage}, nil
			},
		}}

		res, _ := NewRunner(model, tools, testBudget()).Run(context.Background(), "cut cost")
		if res.Termination != TerminatedTokenBudget {
			t.Errorf("%s: termination = %q, want %q", name, res.Termination, TerminatedTokenBudget)
		}
		if res.Plan != nil {
			t.Errorf("%s: a response with unusable usage must not yield a plan", name)
		}
		if res.Spent.OutputTokens < 0 || res.Spent.InputTokens < 0 {
			t.Errorf("%s: spend went negative: %+v", name, res.Spent)
		}
	}
}

func TestRunRejectsNegativeInputEstimate(t *testing.T) {
	model := &fakeModel{estimate: -1, steps: []func(Request) (Response, error){declines("unused")}}

	res, _ := NewRunner(model, &fakeTools{}, testBudget()).Run(context.Background(), "cut cost")
	if res.Termination != TerminatedTokenBudget {
		t.Errorf("termination = %q, want %q", res.Termination, TerminatedTokenBudget)
	}
	if model.calls != 0 {
		t.Errorf("model called %d times, want none", model.calls)
	}
}

func TestRunRejectsInvalidBudgetBeforeCalling(t *testing.T) {
	tests := map[string]func(*Budget){
		"no steps":          func(b *Budget) { b.Steps = 0 },
		"negative steps":    func(b *Budget) { b.Steps = -1 },
		"no input tokens":   func(b *Budget) { b.InputTokens = 0 },
		"no output tokens":  func(b *Budget) { b.OutputTokens = 0 },
		"no wall clock":     func(b *Budget) { b.WallClock = 0 },
		"negative retries":  func(b *Budget) { b.RetriesPerCall = -1 },
		"negative duration": func(b *Budget) { b.WallClock = -time.Second },
	}

	for name, break_ := range tests {
		b := testBudget()
		break_(&b)

		model := &fakeModel{steps: []func(Request) (Response, error){declines("unused")}}
		tools := &fakeTools{}

		_, err := NewRunner(model, tools, b).Run(context.Background(), "cut cost")
		if err == nil {
			t.Errorf("%s: expected an invalid budget to be refused", name)
		}
		if model.calls != 0 || tools.calls != 0 {
			t.Errorf("%s: model %d and tool %d calls, want none", name, model.calls, tools.calls)
		}
	}

	if err := DefaultBudget().validate(); err != nil {
		t.Errorf("DefaultBudget() is not usable: %v", err)
	}
}

func evidenceIDIn(msgs []Message, tool string) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		at := strings.Index(msgs[i].Content, tool+"=")
		if at < 0 {
			continue
		}
		rest := msgs[i].Content[at+len(tool)+1:]
		if end := strings.IndexAny(rest, " ]"); end >= 0 {
			return rest[:end]
		}
	}
	return ""
}

type adaptiveModel struct {
	plan  *plan.Plan
	used  []string
	gotID string
}

func (m *adaptiveModel) EstimateInputTokens(Request) int { return 1 }

func (m *adaptiveModel) Complete(_ context.Context, req Request) (Response, error) {
	id := evidenceIDIn(req.Messages, plan.WorkloadsTool)

	switch {
	case id == "":
		m.used = append(m.used, toolAnalyze)
		return callsTool(toolAnalyze, `{}`)(req)

	case m.gotID == "":
		m.gotID = id
		m.used = append(m.used, toolGetEvidence)
		args := fmt.Sprintf(`{"evidence_id":%q,"path":"workloads.0.candidate_cpu_millicores"}`, id)
		return callsTool(toolGetEvidence, args)(req)

	case strings.Contains(req.Messages[len(req.Messages)-1].Content, `"data":null`):
		m.used = append(m.used, "decline")
		return declines("the workload has no candidate")(req)

	default:
		m.used = append(m.used, "plan")
		return submits(m.plan)(req)
	}
}

func TestRunAdaptsToReturnedEvidence(t *testing.T) {
	rec, p := evidenceAndPlan(t)
	tools := &fakeTools{analyze: returning(rec)}
	model := &adaptiveModel{plan: p}

	res, err := NewRunner(model, tools, testBudget()).Run(context.Background(), "cut cost")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if model.gotID != rec.ID {
		t.Errorf("model used evidence id %q, want the one the tool returned %q", model.gotID, rec.ID)
	}
	want := []string{toolAnalyze, toolGetEvidence, "plan"}
	if len(model.used) != len(want) {
		t.Fatalf("tool path = %v, want %v", model.used, want)
	}
	for i := range want {
		if model.used[i] != want[i] {
			t.Fatalf("tool path = %v, want %v", model.used, want)
		}
	}
	if res.Termination != TerminatedPlan || res.Plan == nil {
		t.Errorf("termination = %q with plan %v, want a validated plan", res.Termination, res.Plan)
	}
}

// The same model declines when the evidence holds no candidate.
func TestRunAdaptsToEvidenceWithoutCandidate(t *testing.T) {
	pod := overProvisionedPod("prod", "api-1", "Deployment", "api")
	pod.CPUP95Available, pod.MemP95Available = false, false
	rec := workloadRecord(t, pod)

	tools := &fakeTools{analyze: returning(rec)}
	model := &adaptiveModel{}

	res, err := NewRunner(model, tools, testBudget()).Run(context.Background(), "cut cost")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.Termination != TerminatedDeclined {
		t.Errorf("termination = %q, want the model to decline on null evidence", res.Termination)
	}
	if got := model.used[len(model.used)-1]; got != "decline" {
		t.Errorf("last choice = %q, want decline", got)
	}
}
