package advisor

import (
	"testing"

	"github.com/tanrikuluozlem/burn/internal/analyzer"
)

func TestCalculateSpotSavings(t *testing.T) {
	tests := []struct {
		name           string
		nodes          []analyzer.NodeCost
		wantApplicable bool
		wantSavings    float64
	}{
		{
			name: "on-demand nodes with low utilization - should recommend spot",
			nodes: []analyzer.NodeCost{
				{Name: "node-1", IsSpot: false, MonthlyPrice: 100, Utilization: 0.40},
				{Name: "node-2", IsSpot: false, MonthlyPrice: 100, Utilization: 0.50},
			},
			wantApplicable: true,
			wantSavings:    140, // (100+100) * 0.70
		},
		{
			name: "on-demand nodes with high utilization - should NOT recommend spot",
			nodes: []analyzer.NodeCost{
				{Name: "node-1", IsSpot: false, MonthlyPrice: 100, Utilization: 0.85},
				{Name: "node-2", IsSpot: false, MonthlyPrice: 100, Utilization: 0.90},
			},
			wantApplicable: false,
			wantSavings:    140, // calculated but not applicable
		},
		{
			name: "all spot nodes - not applicable",
			nodes: []analyzer.NodeCost{
				{Name: "node-1", IsSpot: true, MonthlyPrice: 35, Utilization: 0.40},
				{Name: "node-2", IsSpot: true, MonthlyPrice: 35, Utilization: 0.50},
			},
			wantApplicable: false,
			wantSavings:    0,
		},
		{
			name: "mixed spot and on-demand",
			nodes: []analyzer.NodeCost{
				{Name: "node-1", IsSpot: false, MonthlyPrice: 100, Utilization: 0.40},
				{Name: "node-2", IsSpot: true, MonthlyPrice: 35, Utilization: 0.50},
			},
			wantApplicable: true,
			wantSavings:    70, // only on-demand: 100 * 0.70
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			report := &analyzer.CostReport{Nodes: tt.nodes}
			result := calculateSpotSavings(report)

			if result.Applicable != tt.wantApplicable {
				t.Errorf("Applicable = %v, want %v", result.Applicable, tt.wantApplicable)
			}
			if tt.wantSavings > 0 && result.MonthlySavings != tt.wantSavings {
				t.Errorf("MonthlySavings = %v, want %v", result.MonthlySavings, tt.wantSavings)
			}
		})
	}
}

func TestCalculateConsolidationSavings(t *testing.T) {
	tests := []struct {
		name           string
		nodes          []analyzer.NodeCost
		wantApplicable bool
		wantSavings    float64
	}{
		{
			name: "low utilization cluster - should consolidate",
			nodes: []analyzer.NodeCost{
				{Name: "node-1", MonthlyPrice: 100, Utilization: 0.60},
				{Name: "node-2", MonthlyPrice: 70, Utilization: 0.30}, // least utilized
				{Name: "node-3", MonthlyPrice: 100, Utilization: 0.55},
			},
			wantApplicable: true,
			wantSavings:    70, // remove node-2
		},
		{
			name: "high utilization cluster - should NOT consolidate",
			nodes: []analyzer.NodeCost{
				{Name: "node-1", MonthlyPrice: 100, Utilization: 0.75},
				{Name: "node-2", MonthlyPrice: 100, Utilization: 0.80},
			},
			wantApplicable: false,
		},
		{
			name: "single node - cannot consolidate",
			nodes: []analyzer.NodeCost{
				{Name: "node-1", MonthlyPrice: 100, Utilization: 0.20},
			},
			wantApplicable: false,
		},
		{
			name: "lowest node above 50% - should NOT consolidate",
			nodes: []analyzer.NodeCost{
				{Name: "node-1", MonthlyPrice: 100, Utilization: 0.55},
				{Name: "node-2", MonthlyPrice: 100, Utilization: 0.60},
			},
			wantApplicable: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			report := &analyzer.CostReport{Nodes: tt.nodes}
			result := calculateConsolidationSavings(report)

			if result.Applicable != tt.wantApplicable {
				t.Errorf("Applicable = %v, want %v", result.Applicable, tt.wantApplicable)
			}
			if tt.wantApplicable && result.MonthlySavings != tt.wantSavings {
				t.Errorf("MonthlySavings = %v, want %v", result.MonthlySavings, tt.wantSavings)
			}
		})
	}
}

func TestCalculateRightSizingSavings(t *testing.T) {
	tests := []struct {
		name           string
		nodes          []analyzer.NodeCost
		wantApplicable bool
		wantSavings    float64
		wantNodes      int
	}{
		{
			name: "low memory nodes - should right-size",
			nodes: []analyzer.NodeCost{
				{Name: "node-1", MonthlyPrice: 100, MemRequested: 0.25},
				{Name: "node-2", MonthlyPrice: 100, MemRequested: 0.30},
			},
			wantApplicable: true,
			wantSavings:    100, // (100+100) * 0.50
			wantNodes:      2,
		},
		{
			name: "high memory nodes - should NOT right-size",
			nodes: []analyzer.NodeCost{
				{Name: "node-1", MonthlyPrice: 100, MemRequested: 0.65},
				{Name: "node-2", MonthlyPrice: 100, MemRequested: 0.75},
			},
			wantApplicable: false,
			wantNodes:      0,
		},
		{
			name: "mixed memory usage",
			nodes: []analyzer.NodeCost{
				{Name: "node-1", MonthlyPrice: 100, MemRequested: 0.25}, // low
				{Name: "node-2", MonthlyPrice: 100, MemRequested: 0.70}, // high
			},
			wantApplicable: true,
			wantSavings:    50, // only node-1: 100 * 0.50
			wantNodes:      1,
		},
		{
			name: "exactly at threshold (40%) - should NOT right-size",
			nodes: []analyzer.NodeCost{
				{Name: "node-1", MonthlyPrice: 100, MemRequested: 0.40},
			},
			wantApplicable: false,
			wantNodes:      0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			report := &analyzer.CostReport{Nodes: tt.nodes}
			result := calculateRightSizingSavings(report)

			if result.Applicable != tt.wantApplicable {
				t.Errorf("Applicable = %v, want %v", result.Applicable, tt.wantApplicable)
			}
			if tt.wantApplicable && result.MonthlySavings != tt.wantSavings {
				t.Errorf("MonthlySavings = %v, want %v", result.MonthlySavings, tt.wantSavings)
			}
			if len(result.AffectedNodes) != tt.wantNodes {
				t.Errorf("AffectedNodes count = %v, want %v", len(result.AffectedNodes), tt.wantNodes)
			}
		})
	}
}

func TestTotalSavings(t *testing.T) {
	savings := &PotentialSavings{
		SpotConversion:    &SavingsOpportunity{Applicable: true, MonthlySavings: 100},
		NodeConsolidation: &SavingsOpportunity{Applicable: true, MonthlySavings: 50},
		RightSizing:       &SavingsOpportunity{Applicable: false, MonthlySavings: 30}, // not applicable
	}

	total := savings.TotalSavings()
	if total != 150 {
		t.Errorf("TotalSavings = %v, want 150 (should exclude non-applicable)", total)
	}
}
