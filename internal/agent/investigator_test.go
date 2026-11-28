package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/tanrikuluozlem/burn/internal/advisor"
	"github.com/tanrikuluozlem/burn/internal/analyzer"
	"github.com/tanrikuluozlem/burn/internal/billing"
	"github.com/tanrikuluozlem/burn/internal/plan"
)

// reportFixture is over-provisioned enough to produce both rightsizing and spot
// figures.
func reportFixture() *analyzer.CostReport {
	pod := analyzer.PodEfficiency{
		Name:            "api",
		Namespace:       "prod",
		CPURequest:      2000,
		CPUUsage:        0.3,
		CPUEfficiency:   0.15,
		CPUCost:         40,
		CPUP95Usage:     0.3,
		CPUP95Available: true,
		MemRequest:      4 << 30,
		MemUsage:        1 << 30,
		MemEfficiency:   0.25,
		RAMCost:         20,
		MemoryP95Usage:  1 << 30,
		MemP95Available: true,
		MonthlyCost:     60,
	}

	return &analyzer.CostReport{
		MetricsSource:   "prometheus",
		MonthlyCost:     300,
		Nodes:           []analyzer.NodeCost{{Name: "node-a", MonthlyPrice: 150}, {Name: "node-b", MonthlyPrice: 150}},
		AllPods:         []analyzer.PodEfficiency{pod},
		InefficientPods: []analyzer.PodEfficiency{pod},
		SpotSavings:     90,
	}
}

type savingsPayload struct {
	Total         float64 `json:"total"`
	Spot          float64 `json:"spot"`
	Consolidation float64 `json:"consolidation"`
	RightSizing   float64 `json:"right_sizing"`
}

func TestSavingsEvidenceMatchesEngine(t *testing.T) {
	report := reportFixture()

	e, err := savingsEvidence(advisor.CalculateSavings(report, advisor.DefaultSavingsConfig()))
	if err != nil {
		t.Fatalf("savingsEvidence() error = %v", err)
	}

	var got savingsPayload
	if err := json.Unmarshal(e.Data, &got); err != nil {
		t.Fatalf("unmarshal evidence: %v", err)
	}

	want := advisor.CalculateSavings(report, advisor.DefaultSavingsConfig())
	if got.Total != want.TotalSavings() {
		t.Errorf("total = %v, want %v", got.Total, want.TotalSavings())
	}
	if got.Spot != want.ApplicableSavings(want.SpotConversion) {
		t.Errorf("spot = %v, want %v", got.Spot, want.ApplicableSavings(want.SpotConversion))
	}
	if got.RightSizing != want.ApplicableSavings(want.RightSizing) {
		t.Errorf("right_sizing = %v, want %v", got.RightSizing, want.ApplicableSavings(want.RightSizing))
	}
	if got.RightSizing == 0 {
		t.Error("fixture produced no rightsizing savings, so the test proves nothing")
	}
}

func TestSavingsEvidenceToolName(t *testing.T) {
	report := reportFixture()
	e, err := savingsEvidence(advisor.CalculateSavings(report, advisor.DefaultSavingsConfig()))
	if err != nil {
		t.Fatalf("savingsEvidence() error = %v", err)
	}
	if e.Tool != plan.SavingsTool {
		t.Errorf("tool = %q, want %q", e.Tool, plan.SavingsTool)
	}
}

func TestCostEvidenceCarriesTheReport(t *testing.T) {
	report := reportFixture()

	e, err := costEvidence(report)
	if err != nil {
		t.Fatalf("costEvidence() error = %v", err)
	}
	if e.Tool != toolAnalyze {
		t.Errorf("tool = %q, want %q", e.Tool, toolAnalyze)
	}

	var got analyzer.CostReport
	if err := json.Unmarshal(e.Data, &got); err != nil {
		t.Fatalf("unmarshal evidence: %v", err)
	}
	if got.MonthlyCost != report.MonthlyCost || len(got.Nodes) != len(report.Nodes) {
		t.Errorf("evidence does not round-trip the report: %+v", got)
	}
}

func TestEvidenceIDIsContentAddressed(t *testing.T) {
	first, err := newEvidence("analyze", map[string]float64{"cost": 1})
	if err != nil {
		t.Fatalf("newEvidence() error = %v", err)
	}
	same, _ := newEvidence("analyze", map[string]float64{"cost": 1})
	otherData, _ := newEvidence("analyze", map[string]float64{"cost": 2})
	otherTool, _ := newEvidence("spot_readiness", map[string]float64{"cost": 1})

	if first.ID != same.ID {
		t.Errorf("identical results got different ids: %s and %s", first.ID, same.ID)
	}
	if first.ID == otherData.ID {
		t.Error("different data produced the same id")
	}
	if first.ID == otherTool.ID {
		t.Error("different tools produced the same id")
	}
}

func TestReconcileUnavailableWithoutCURConfig(t *testing.T) {
	tools := NewTools(Config{}, nil)

	_, err := tools.Reconcile(context.Background(), ReconcileInput{Provider: "aws"})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}

func TestReconcileUnavailableWithoutAzureSubscription(t *testing.T) {
	tools := NewTools(Config{}, nil)

	_, err := tools.Reconcile(context.Background(), ReconcileInput{Provider: "azure"})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}

func TestConfiguredProvidersAreAvailable(t *testing.T) {
	aws := NewTools(Config{Athena: billing.AthenaConfig{
		Database:       "cur_db",
		Table:          "cur_table",
		OutputLocation: "s3://bucket/prefix",
	}}, nil)
	if err := aws.checkBillingConfig("aws"); err != nil {
		t.Errorf("configured CUR reported unavailable: %v", err)
	}

	azure := NewTools(Config{Azure: billing.AzureConfig{SubscriptionID: "sub-1"}}, nil)
	if err := azure.checkBillingConfig("azure"); err != nil {
		t.Errorf("configured Azure reported unavailable: %v", err)
	}
}

func TestAnalyzeRejectsInvalidPeriod(t *testing.T) {
	tools := NewTools(Config{}, nil)

	_, err := tools.Analyze(context.Background(), AnalyzeInput{Period: "7 days"})
	if err == nil {
		t.Fatal("expected an invalid period to be rejected before collecting")
	}
}
