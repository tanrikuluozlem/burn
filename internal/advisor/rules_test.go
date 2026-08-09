package advisor

import (
	"testing"

	"github.com/tanrikuluozlem/burn/internal/analyzer"
)

func TestCalculateSpotSavings(t *testing.T) {
	tests := []struct {
		name           string
		spotSavings    float64
		wantApplicable bool
		wantSavings    float64
	}{
		{
			name:           "eligible workloads with savings",
			spotSavings:    36.43,
			wantApplicable: true,
			wantSavings:    36.43,
		},
		{
			name:           "no spot-ready workloads",
			spotSavings:    0,
			wantApplicable: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			report := &analyzer.CostReport{SpotSavings: tt.spotSavings}
			result := calculateSpotSavings(report)

			if result.Applicable != tt.wantApplicable {
				t.Errorf("Applicable = %v, want %v", result.Applicable, tt.wantApplicable)
			}
			if tt.wantApplicable && result.MonthlySavings != tt.wantSavings {
				t.Errorf("MonthlySavings = %v, want %v", result.MonthlySavings, tt.wantSavings)
			}
		})
	}
}

func TestSpotSavingsUsesEligibleWorkloads(t *testing.T) {
	// SpotConversion must equal report.SpotSavings, NOT fleet-level node cost × discount
	report := &analyzer.CostReport{
		SpotSavings: 36.43,
		Nodes: []analyzer.NodeCost{
			{Name: "node-1", IsSpot: false, MonthlyPrice: 100},
			{Name: "node-2", IsSpot: false, MonthlyPrice: 100},
		},
	}
	result := calculateSpotSavings(report)

	if result.MonthlySavings != 36.43 {
		t.Errorf("SpotConversion = %.2f, want 36.43 (eligible workloads only, not %.2f fleet-level)", result.MonthlySavings, 200*0.79)
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
			report := &analyzer.CostReport{Nodes: tt.nodes, MetricsSource: "requests"}
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

func TestPodRightSizingP95Availability(t *testing.T) {
	tests := []struct {
		name           string
		pods           []analyzer.PodEfficiency
		wantApplicable bool
		wantSavings    float64
	}{
		{
			name: "both CPU and MEM P95 available",
			pods: []analyzer.PodEfficiency{{
				CPURequest: 1000, CPUUsage: 0.1, CPUEfficiency: 0.10, CPUCost: 20,
				MemRequest: 1 << 30, MemUsage: 100 << 20, MemEfficiency: 0.10, RAMCost: 10,
				CPUP95Usage: 0.2, CPUP95Available: true,
				MemoryP95Usage: 200 << 20, MemP95Available: true,
			}},
			wantApplicable: true,
		},
		{
			name: "CPU P95 available, MEM P95 unavailable — CPU only",
			pods: []analyzer.PodEfficiency{{
				CPURequest: 1000, CPUUsage: 0.1, CPUEfficiency: 0.10, CPUCost: 20,
				MemRequest: 1 << 30, MemUsage: 100 << 20, MemEfficiency: 0.10, RAMCost: 10,
				CPUP95Usage: 0.2, CPUP95Available: true,
				MemP95Available: false,
			}},
			wantApplicable: true,
		},
		{
			name: "CPU P95 unavailable, MEM P95 available — MEM only",
			pods: []analyzer.PodEfficiency{{
				CPURequest: 1000, CPUUsage: 0.1, CPUEfficiency: 0.10, CPUCost: 20,
				MemRequest: 1 << 30, MemUsage: 100 << 20, MemEfficiency: 0.10, RAMCost: 10,
				CPUP95Available: false,
				MemoryP95Usage:  200 << 20, MemP95Available: true,
			}},
			wantApplicable: true,
		},
		{
			name: "neither P95 available — no recommendation",
			pods: []analyzer.PodEfficiency{{
				CPURequest: 1000, CPUUsage: 0.1, CPUEfficiency: 0.10, CPUCost: 20,
				MemRequest: 1 << 30, MemUsage: 100 << 20, MemEfficiency: 0.10, RAMCost: 10,
				CPUP95Available: false, MemP95Available: false,
			}},
			wantApplicable: false,
		},
		{
			name: "CPU P95 available but zero, avg > 0 — no CPU recommendation",
			pods: []analyzer.PodEfficiency{{
				CPURequest: 1000, CPUUsage: 0.1, CPUEfficiency: 0.10, CPUCost: 20,
				MemRequest: 1 << 30, MemUsage: 100 << 20, MemEfficiency: 0.10, RAMCost: 10,
				CPUP95Usage: 0, CPUP95Available: true,
				MemP95Available: false,
			}},
			wantApplicable: false, // CPU P95=0 suppresses, MEM unavailable
		},
		{
			name: "MEM P95 available but zero, avg > 0 — no MEM recommendation",
			pods: []analyzer.PodEfficiency{{
				CPURequest: 1000, CPUUsage: 0.1, CPUEfficiency: 0.10, CPUCost: 20,
				MemRequest: 1 << 30, MemUsage: 100 << 20, MemEfficiency: 0.10, RAMCost: 10,
				CPUP95Available: false,
				MemoryP95Usage:  0, MemP95Available: true,
			}},
			wantApplicable: false,
		},
		{
			name: "pod missing from P95 map — no recommendation",
			pods: []analyzer.PodEfficiency{{
				CPURequest: 1000, CPUUsage: 0.1, CPUEfficiency: 0.10, CPUCost: 20,
				MemRequest: 1 << 30, MemUsage: 100 << 20, MemEfficiency: 0.10, RAMCost: 10,
				// both Available=false (pod absent from P95 query results)
			}},
			wantApplicable: false,
		},
		{
			name: "valid P95 — recommendation uses P95 * 1.5",
			pods: []analyzer.PodEfficiency{{
				CPURequest: 1000, CPUUsage: 0.1, CPUEfficiency: 0.10, CPUCost: 20,
				MemRequest: 1 << 30, MemUsage: 100 << 20, MemEfficiency: 0.50, RAMCost: 10,
				CPUP95Usage: 0.2, CPUP95Available: true,
				MemP95Available: false,
			}},
			wantApplicable: true,
			wantSavings:    14, // CPUCost * (1 - 300/1000) = 20 * 0.70 = 14
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			report := &analyzer.CostReport{
				MetricsSource: "prometheus",
				Period:        "7d",
				AllPods:       tt.pods,
			}
			result := calculatePodRightSizingSavings(report)
			if result.Applicable != tt.wantApplicable {
				t.Errorf("Applicable = %v, want %v (reason: %s)", result.Applicable, tt.wantApplicable, result.Reason)
			}
			if tt.wantSavings > 0 && result.MonthlySavings != tt.wantSavings {
				t.Errorf("MonthlySavings = %.2f, want %.2f", result.MonthlySavings, tt.wantSavings)
			}
		})
	}
}

func TestRightSizingNoperiodSuppression(t *testing.T) {
	// Instant metrics only — InefficientPods exist but no P95 available
	report := &analyzer.CostReport{
		MetricsSource: "prometheus",
		AllPods: []analyzer.PodEfficiency{{
			CPURequest: 1000, CPUUsage: 0.1, CPUEfficiency: 0.10, CPUCost: 20,
			MemRequest: 1 << 30, MemUsage: 100 << 20, MemEfficiency: 0.10, RAMCost: 10,
			// No P95 available — instant only
		}},
	}
	result := calculatePodRightSizingSavings(report)
	if result.Applicable {
		t.Errorf("should not produce rightsizing from instant metrics (no P95)")
	}
}

func TestRightSizingNodeFallbackUnchanged(t *testing.T) {
	report := &analyzer.CostReport{
		MetricsSource: "requests",
		Nodes: []analyzer.NodeCost{
			{Name: "node-1", CPURequested: 0.20, MemRequested: 0.20, MonthlyPrice: 100},
		},
	}
	result := calculateRightSizingSavings(report)
	if !result.Applicable {
		t.Error("node-level rightsizing should still work without Prometheus")
	}
	if result.MonthlySavings != 50 {
		t.Errorf("MonthlySavings = %.2f, want 50", result.MonthlySavings)
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
