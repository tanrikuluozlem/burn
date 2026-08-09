package advisor

import (
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
