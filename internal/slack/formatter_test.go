package slack

import (
	"strings"
	"testing"

	"github.com/ozlemtanrikulu/burn/internal/advisor"
	"github.com/ozlemtanrikulu/burn/internal/analyzer"
)

func TestFormatCostReport(t *testing.T) {
	report := &analyzer.CostReport{
		TotalNodes:  2,
		TotalPods:   10,
		HourlyCost:  0.50,
		MonthlyCost: 365.00,
		Nodes: []analyzer.NodeCost{
			{Name: "node-1", InstanceType: "t3.medium", IsSpot: true, Utilization: 0.75, MonthlyPrice: 100},
			{Name: "node-2", InstanceType: "t3.large", IsSpot: false, Utilization: 0.30, MonthlyPrice: 200},
		},
	}

	msg := FormatCostReport(report)

	if len(msg.Blocks) < 2 {
		t.Error("expected at least 2 blocks")
	}
	if msg.Blocks[0].Type != "header" {
		t.Error("first block should be header")
	}
}

func TestFormatCostReportWithWaste(t *testing.T) {
	report := &analyzer.CostReport{
		TotalNodes:  1,
		TotalPods:   5,
		HourlyCost:  0.10,
		MonthlyCost: 73.00,
		WasteAnalysis: analyzer.WasteAnalysis{
			PotentialSavings: 50.0,
			UnderutilizedNodes: []analyzer.UnderutilizedNode{
				{Name: "idle-node", Utilization: 0.05, Recommendation: "remove"},
			},
		},
	}

	msg := FormatCostReport(report)

	found := false
	for _, b := range msg.Blocks {
		if b.Text != nil && strings.Contains(b.Text.Text, "Waste") {
			found = true
		}
	}
	if !found {
		t.Error("expected waste analysis block")
	}
}

func TestFormatAIReport(t *testing.T) {
	report := &advisor.Report{
		Summary:               "Cluster is over-provisioned",
		TotalPotentialSavings: 150.0,
		Recommendations: []advisor.Recommendation{
			{Title: "Downsize nodes", Description: "Use smaller instances", Severity: advisor.SeverityHigh},
		},
	}

	msg := FormatAIReport(report)

	if len(msg.Blocks) < 3 {
		t.Errorf("expected at least 3 blocks, got %d", len(msg.Blocks))
	}
}

func TestFormatQuickCost(t *testing.T) {
	report := &analyzer.CostReport{
		TotalNodes:  3,
		TotalPods:   20,
		HourlyCost:  1.0,
		MonthlyCost: 730.0,
	}

	msg := FormatQuickCost(report)

	if len(msg.Blocks) != 1 {
		t.Errorf("expected 1 block, got %d", len(msg.Blocks))
	}
	if !strings.Contains(msg.Blocks[0].Text.Text, "730") {
		t.Error("should contain monthly cost")
	}
}

func TestSeverityEmoji(t *testing.T) {
	tests := []struct {
		sev  advisor.Severity
		want string
	}{
		{advisor.SeverityCritical, ":red_circle:"},
		{advisor.SeverityHigh, ":large_orange_circle:"},
		{advisor.SeverityMedium, ":large_yellow_circle:"},
		{advisor.SeverityLow, ":white_circle:"},
	}

	for _, tc := range tests {
		got := severityEmoji(tc.sev)
		if got != tc.want {
			t.Errorf("severityEmoji(%v) = %s, want %s", tc.sev, got, tc.want)
		}
	}
}

func TestTruncate(t *testing.T) {
	if truncate("short", 10) != "short" {
		t.Error("should not truncate short strings")
	}
	if truncate("this is a very long string", 10) != "this is..." {
		t.Error("should truncate with ellipsis")
	}
}
