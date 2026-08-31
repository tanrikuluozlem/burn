package advisor

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tanrikuluozlem/burn/internal/analyzer"
)

func TestSystemPromptConstraints(t *testing.T) {
	checks := []struct {
		name   string
		substr string
	}{
		{"no savings in prose", "Do not include dollar savings amounts in summary, title, description, or action"},
		{"no financial ranking", "Do not rank recommendations by savings amount"},
		{"no invented thresholds", "Do not invent thresholds"},
		{"observation vs inference", "Distinguish observation from inference"},
		{"PDB not Spot protection", "do NOT prevent cloud-provider Spot reclamation"},
		{"PDB resilience not guarantee", "do not guarantee availability during interruption"},
		{"unmatched not safe to remove", "not assumed orphaned or safe to remove"},
		{"no invented resource targets", "do not recommend specific CPU/memory request values"},
		{"estimated_savings must be 0", "estimated_savings must be 0"},
		{"action read-only", "action field must contain only read-only commands (kubectl get, describe, logs)"},
		{"no mutating commands", "Do not generate mutating commands"},
		{"kubectl top not P95", "kubectl top shows current usage, not historical P95"},
	}

	for _, c := range checks {
		if !strings.Contains(systemPrompt, c.substr) {
			t.Errorf("system prompt missing %q constraint: expected substring %q", c.name, c.substr)
		}
	}

	if strings.Contains(systemPrompt, "pick one strategy") || strings.Contains(systemPrompt, "Pick one strategy") {
		t.Error("system prompt should not contain 'pick ONE strategy'")
	}
	if strings.Contains(systemPrompt, "dollar impact") {
		t.Error("system prompt should not instruct model to lead with dollar impact")
	}
}

func TestAskPromptConstraints(t *testing.T) {
	checks := []struct {
		name   string
		substr string
	}{
		{"no own calculations", "Do NOT calculate your own values"},
		{"no invented resource targets", "Do not invent specific CPU/memory request targets"},
		{"observation vs inference", "Distinguish observation from inference"},
		{"PDB not Spot protection", "do NOT prevent cloud-provider Spot reclamation"},
		{"no assumed cluster state", "Do not assume cluster state"},
		{"idle node not auto-removable", "not automatically safe to drain or remove"},
		{"idle cost not realizable savings", "NOT the same as realizable savings"},
		{"no unsupported causal claims", "Do not claim that over-provisioned pod requests are causing nodes to remain running"},
		{"read-only commands", "read-only investigation commands"},
		{"no mutating commands", "Do not generate mutating commands"},
		{"prefer complete answer", "Prefer a shorter complete answer"},
		{"no invented limits", "Burn computes request targets only"},
		{"no cost-as-waste relabeling", "Do not label pod allocated cost as waste"},
		{"no ungrounded financial ranking", "Do not rank opportunities by dollar impact"},
		{"no priority-implying order", "Do not number categories in a way that implies priority"},
		{"no relative actionability", "Do not assign relative priority, actionability, or preference"},
		{"no unsupported universal claims", "Do not use \"all\", \"every\", or \"none\" unless every relevant item"},
		{"no invented thresholds", "Do not invent thresholds, floors, minimums"},
		{"no alternative target", "Do not suggest a different numeric request target"},
		{"conditional workload ownership", "Do not assert workload ownership"},
		{"per-pod target availability", "Rightsizing targets are per-pod and per-resource"},
		{"request not runtime cap", "requests affect scheduler placement, not runtime CPU caps"},
		{"no name-based ownership", "Do not infer pod ownership or controller state from resource names"},
		{"separate cpu mem efficiency", "Keep CPU and memory efficiency claims separate"},
		{"reconcile top-level variance", "Do not promote internal sub-components"},
		{"no volume-snapshot confusion", "Do not describe an EBS volume as a snapshot"},
		{"management fee label only", "Do not infer contract type, support tier"},
	}

	for _, c := range checks {
		if !strings.Contains(askSystemPrompt, c.substr) {
			t.Errorf("ask prompt missing %q constraint: expected substring %q", c.name, c.substr)
		}
	}
}

func TestP95PromptInstructionsIntact(t *testing.T) {
	if !strings.Contains(systemPrompt, "When p95 data is shown") {
		t.Error("system prompt missing P95-available instruction")
	}
	if !strings.Contains(systemPrompt, "When no p95 data is shown") {
		t.Error("system prompt missing no-P95 instruction")
	}
}

func TestBuildPromptNoSavingsValues(t *testing.T) {
	report := &analyzer.CostReport{
		MetricsSource: "prometheus",
		Nodes: []analyzer.NodeCost{
			{Name: "node-1", MonthlyPrice: 100, IdlePercent: 0.40},
		},
		Namespaces: []analyzer.NamespaceCost{
			{Name: "default", PodCount: 1, MonthlyCost: 50},
		},
	}
	prompt := buildPrompt(report)

	if strings.Contains(prompt, "PRE-CALCULATED SAVINGS") {
		t.Error("buildPrompt must not contain PRE-CALCULATED SAVINGS section")
	}
	if strings.Contains(prompt, "Use $") && strings.Contains(prompt, "for estimated_savings") {
		t.Error("buildPrompt must not supply a dollar value for estimated_savings")
	}
	if !strings.Contains(prompt, "NODE SUMMARY") {
		t.Error("buildPrompt must contain NODE SUMMARY")
	}
}

func TestReportJSON_BackwardCompatAndNewFields(t *testing.T) {
	r := &Report{
		TotalPotentialSavings: 170,
		SpotSavings:           40,
		ConsolidationSavings:  30,
		RightSizingSavings:    170,
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)

	if !strings.Contains(s, `"total_potential_savings"`) {
		t.Error("JSON must contain total_potential_savings for backward compat")
	}
	if !strings.Contains(s, `"spot_savings"`) {
		t.Error("JSON must contain spot_savings")
	}
	if !strings.Contains(s, `"consolidation_savings"`) {
		t.Error("JSON must contain consolidation_savings")
	}
	if !strings.Contains(s, `"rightsizing_savings"`) {
		t.Error("JSON must contain rightsizing_savings")
	}
}

func TestReportJSON_OmitEmptyNewFields(t *testing.T) {
	r := &Report{
		TotalPotentialSavings: 0,
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)

	if strings.Contains(s, `"spot_savings"`) {
		t.Error("spot_savings should be omitted when zero")
	}
	if strings.Contains(s, `"consolidation_savings"`) {
		t.Error("consolidation_savings should be omitted when zero")
	}
	if strings.Contains(s, `"rightsizing_savings"`) {
		t.Error("rightsizing_savings should be omitted when zero")
	}
	if !strings.Contains(s, `"total_potential_savings"`) {
		t.Error("total_potential_savings must always be present (no omitempty)")
	}
}

func TestAskPromptP95Headroom(t *testing.T) {
	if !strings.Contains(askSystemPrompt, "Burn-computed rightsizing") {
		t.Error("askSystemPrompt must reference Burn-computed rightsizing values")
	}
	if !strings.Contains(askSystemPrompt, "Do not calculate your own request targets") {
		t.Error("askSystemPrompt must prohibit model-calculated request targets")
	}
	if !strings.Contains(askSystemPrompt, "When no p95 data is shown") {
		t.Error("askSystemPrompt must instruct against request targets without p95")
	}
}

func TestBuildAskPromptP95Available(t *testing.T) {
	report := &analyzer.CostReport{
		Period:        "7d",
		MetricsSource: "prometheus",
	}
	prompt := (&Advisor{}).buildAskPrompt(report, "test question")
	if !strings.Contains(prompt, "P95 values are available") {
		t.Error("buildAskPrompt must indicate P95 availability when period is set")
	}
}

func TestBuildAskPromptP95Unavailable(t *testing.T) {
	report := &analyzer.CostReport{
		MetricsSource: "prometheus",
	}
	prompt := (&Advisor{}).buildAskPrompt(report, "test question")
	if !strings.Contains(prompt, "no P95 available") {
		t.Error("buildAskPrompt must indicate P95 unavailability for instant metrics")
	}
}

func TestBuildAskPromptRightsizing(t *testing.T) {
	report := &analyzer.CostReport{
		Period:        "7d",
		MetricsSource: "prometheus",
		InefficientPods: []analyzer.PodEfficiency{{
			Name: "web", Namespace: "prod",
			CPUP95Available: true, CPUP95Usage: 0.000121,
			MemP95Available: true, MemoryP95Usage: 3565158,
		}},
	}
	prompt := (&Advisor{}).buildAskPrompt(report, "test")
	if !strings.Contains(prompt, "CPU 1m") {
		t.Error("expected Burn-computed CPU 1m")
	}
	if !strings.Contains(prompt, "MEM 6Mi") {
		t.Error("expected Burn-computed MEM 6Mi")
	}
	if !strings.Contains(prompt, "rounded up") {
		t.Error("rightsizing targets must include p95×1.5 provenance")
	}
}

func TestBuildAskPromptRightsizingCPUOnly(t *testing.T) {
	report := &analyzer.CostReport{
		Period:        "7d",
		MetricsSource: "prometheus",
		InefficientPods: []analyzer.PodEfficiency{{
			Name: "api", Namespace: "prod",
			CPUP95Available: true, CPUP95Usage: 0.0052,
			MemP95Available: false,
		}},
	}
	prompt := (&Advisor{}).buildAskPrompt(report, "test")
	if !strings.Contains(prompt, "CPU 8m") {
		t.Error("expected Burn-computed CPU 8m")
	}
	if strings.Contains(prompt, "MEM") && strings.Contains(prompt, "Mi") && strings.Contains(prompt, "prod/api") {
		t.Error("should not contain memory recommendation without P95")
	}
}

func TestBuildAskPromptRightsizingMemOnly(t *testing.T) {
	report := &analyzer.CostReport{
		Period:        "7d",
		MetricsSource: "prometheus",
		InefficientPods: []analyzer.PodEfficiency{{
			Name: "cache", Namespace: "prod",
			CPUP95Available: false,
			MemP95Available: true, MemoryP95Usage: 8073216,
		}},
	}
	prompt := (&Advisor{}).buildAskPrompt(report, "test")
	if !strings.Contains(prompt, "MEM 12Mi") {
		t.Error("expected Burn-computed MEM 12Mi")
	}
	if strings.Contains(prompt, "CPU") && strings.Contains(prompt, "prod/cache: recommend CPU") {
		t.Error("should not contain CPU recommendation without P95")
	}
}

func TestBuildAskPromptNoRightsizingWithoutPeriod(t *testing.T) {
	report := &analyzer.CostReport{
		MetricsSource: "prometheus",
		InefficientPods: []analyzer.PodEfficiency{{
			Name: "web", Namespace: "prod",
			CPUP95Available: true, CPUP95Usage: 0.5,
			MemP95Available: true, MemoryP95Usage: 100 << 20,
		}},
	}
	prompt := (&Advisor{}).buildAskPrompt(report, "test")
	if strings.Contains(prompt, "RIGHTSIZING") {
		t.Error("should not include rightsizing without period")
	}
}

func TestBuildAskPromptOpportunities(t *testing.T) {
	report := &analyzer.CostReport{
		TotalIdleCost: 113.98,
		SpotSavings:   36.43,
		WasteAnalysis: analyzer.WasteAnalysis{PotentialSavings: 37.14},
	}
	prompt := (&Advisor{}).buildAskPrompt(report, "test")
	if !strings.Contains(prompt, "BURN-REPORTED OPPORTUNITIES") {
		t.Error("buildAskPrompt must include unordered opportunities section")
	}
	if !strings.Contains(prompt, "unordered") {
		t.Error("opportunities section must state unordered")
	}
	if !strings.Contains(prompt, "$113.98") {
		t.Error("idle cost must use Burn-provided value")
	}
	if !strings.Contains(prompt, "$36.43") {
		t.Error("spot savings must use Burn-provided value")
	}
	if !strings.Contains(prompt, "$37.14") {
		t.Error("consolidation potential must use Burn-provided value")
	}
}

func TestBuildAskPromptNoOpportunitiesWhenEmpty(t *testing.T) {
	report := &analyzer.CostReport{}
	prompt := (&Advisor{}).buildAskPrompt(report, "test")
	if strings.Contains(prompt, "BURN-REPORTED OPPORTUNITIES") {
		t.Error("should not include opportunities when all values are zero")
	}
}
