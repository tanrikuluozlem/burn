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
			name: "on-demand nodes with high idle - should recommend spot",
			nodes: []analyzer.NodeCost{
				{Name: "node-1", IsSpot: false, MonthlyPrice: 100, IdlePercent: 0.60}, // 40% used
				{Name: "node-2", IsSpot: false, MonthlyPrice: 100, IdlePercent: 0.50}, // 50% used
			},
			wantApplicable: true,
			wantSavings:    130, // (100+100) * 0.65
		},
		{
			name: "on-demand nodes with low idle - still applicable (spot is workload-dependent)",
			nodes: []analyzer.NodeCost{
				{Name: "node-1", IsSpot: false, MonthlyPrice: 100, IdlePercent: 0.15}, // 85% used
				{Name: "node-2", IsSpot: false, MonthlyPrice: 100, IdlePercent: 0.10}, // 90% used
			},
			wantApplicable: true,
			wantSavings:    130,
		},
		{
			name: "all spot nodes - not applicable",
			nodes: []analyzer.NodeCost{
				{Name: "node-1", IsSpot: true, MonthlyPrice: 35, IdlePercent: 0.60},
				{Name: "node-2", IsSpot: true, MonthlyPrice: 35, IdlePercent: 0.50},
			},
			wantApplicable: false,
			wantSavings:    0,
		},
		{
			name: "mixed spot and on-demand",
			nodes: []analyzer.NodeCost{
				{Name: "node-1", IsSpot: false, MonthlyPrice: 100, IdlePercent: 0.60}, // 40% used
				{Name: "node-2", IsSpot: true, MonthlyPrice: 35, IdlePercent: 0.50},
			},
			wantApplicable: true,
			wantSavings:    65, // only on-demand: 100 * 0.65
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
			name: "high idle cluster - should consolidate",
			nodes: []analyzer.NodeCost{
				{Name: "node-1", MonthlyPrice: 100, IdlePercent: 0.40}, // 60% used
				{Name: "node-2", MonthlyPrice: 70, IdlePercent: 0.70},  // 30% used - most idle
				{Name: "node-3", MonthlyPrice: 100, IdlePercent: 0.45}, // 55% used
			},
			wantApplicable: true,
			wantSavings:    70, // remove node-2
		},
		{
			name: "low idle cluster - should NOT consolidate",
			nodes: []analyzer.NodeCost{
				{Name: "node-1", MonthlyPrice: 100, IdlePercent: 0.25}, // 75% used
				{Name: "node-2", MonthlyPrice: 100, IdlePercent: 0.20}, // 80% used
			},
			wantApplicable: false,
		},
		{
			name: "single node - cannot consolidate",
			nodes: []analyzer.NodeCost{
				{Name: "node-1", MonthlyPrice: 100, IdlePercent: 0.80}, // 20% used
			},
			wantApplicable: false,
		},
		{
			name: "most idle node below 50% - should NOT consolidate",
			nodes: []analyzer.NodeCost{
				{Name: "node-1", MonthlyPrice: 100, IdlePercent: 0.45}, // 55% used
				{Name: "node-2", MonthlyPrice: 100, IdlePercent: 0.40}, // 60% used
			},
			wantApplicable: false,
		},
		{
			name: "remaining nodes cannot absorb workload - should NOT consolidate",
			nodes: []analyzer.NodeCost{
				{Name: "node-1", MonthlyPrice: 100, IdlePercent: 0.55}, // 45% used - most idle
				{Name: "node-2", MonthlyPrice: 100, IdlePercent: 0.35}, // 65% used
			},
			// Remove node-1: node-2 would become 65% + 45% = 110% → overloaded
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
			name: "low CPU and memory - should right-size",
			nodes: []analyzer.NodeCost{
				{Name: "node-1", MonthlyPrice: 100, CPURequested: 0.20, MemRequested: 0.25},
				{Name: "node-2", MonthlyPrice: 100, CPURequested: 0.30, MemRequested: 0.30},
			},
			wantApplicable: true,
			wantSavings:    100, // (100+100) * 0.50
			wantNodes:      2,
		},
		{
			name: "high memory - should NOT right-size",
			nodes: []analyzer.NodeCost{
				{Name: "node-1", MonthlyPrice: 100, CPURequested: 0.20, MemRequested: 0.65},
				{Name: "node-2", MonthlyPrice: 100, CPURequested: 0.30, MemRequested: 0.75},
			},
			wantApplicable: false,
			wantNodes:      0,
		},
		{
			name: "low memory but high CPU - should NOT right-size",
			nodes: []analyzer.NodeCost{
				{Name: "node-1", MonthlyPrice: 100, CPURequested: 0.85, MemRequested: 0.25},
			},
			wantApplicable: false,
			wantNodes:      0,
		},
		{
			name: "mixed nodes - only low-util node right-sized",
			nodes: []analyzer.NodeCost{
				{Name: "node-1", MonthlyPrice: 100, CPURequested: 0.20, MemRequested: 0.25},
				{Name: "node-2", MonthlyPrice: 100, CPURequested: 0.70, MemRequested: 0.70},
			},
			wantApplicable: true,
			wantSavings:    50, // only node-1: 100 * 0.50
			wantNodes:      1,
		},
		{
			name: "exactly at threshold (40%) - should NOT right-size",
			nodes: []analyzer.NodeCost{
				{Name: "node-1", MonthlyPrice: 100, CPURequested: 0.40, MemRequested: 0.40},
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
	if total != 100 {
		t.Errorf("TotalSavings = %v, want 100 (should return max of applicable strategies)", total)
	}
}

