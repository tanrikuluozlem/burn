package advisor

import (
	"github.com/tanrikuluozlem/burn/internal/analyzer"
)

// PotentialSavings contains pre-calculated savings opportunities
type PotentialSavings struct {
	SpotConversion    *SavingsOpportunity
	NodeConsolidation *SavingsOpportunity
	RightSizing       *SavingsOpportunity
}

// SavingsOpportunity represents a calculated savings opportunity
type SavingsOpportunity struct {
	Type           string
	MonthlySavings float64
	Applicable     bool
	Reason         string
	AffectedNodes  []string
}

// CalculateSavings computes deterministic savings opportunities from the cost report
func CalculateSavings(report *analyzer.CostReport) *PotentialSavings {
	savings := &PotentialSavings{}

	// 1. Spot conversion: ~70% savings on on-demand instances
	savings.SpotConversion = calculateSpotSavings(report)

	// 2. Node consolidation: remove underutilized nodes
	savings.NodeConsolidation = calculateConsolidationSavings(report)

	// 3. Right-sizing: downsize overprovisioned nodes
	savings.RightSizing = calculateRightSizingSavings(report)

	return savings
}

func calculateSpotSavings(report *analyzer.CostReport) *SavingsOpportunity {
	var onDemandCost float64
	var onDemandNodes []string

	for _, node := range report.Nodes {
		if !node.IsSpot {
			onDemandCost += node.MonthlyPrice
			onDemandNodes = append(onDemandNodes, node.Name)
		}
	}

	if len(onDemandNodes) == 0 {
		return &SavingsOpportunity{
			Type:       "spot_conversion",
			Applicable: false,
			Reason:     "All nodes are already spot instances",
		}
	}

	spotDiscount := 0.65
	monthlySavings := onDemandCost * spotDiscount

	return &SavingsOpportunity{
		Type:           "spot_conversion",
		MonthlySavings: monthlySavings,
		Applicable:     true,
		Reason:         "Max potential savings if stateless workloads moved to spot (65% discount)",
		AffectedNodes:  onDemandNodes,
	}
}

func calculateConsolidationSavings(report *analyzer.CostReport) *SavingsOpportunity {
	if len(report.Nodes) < 2 {
		return &SavingsOpportunity{
			Type:       "node_consolidation",
			Applicable: false,
			Reason:     "Only one node, cannot consolidate",
		}
	}

	var mostIdleNode *analyzer.NodeCost
	var highestIdle float64 = 0.0

	for i := range report.Nodes {
		node := &report.Nodes[i]
		if node.IdlePercent > highestIdle {
			highestIdle = node.IdlePercent
			mostIdleNode = node
		}
	}

	if highestIdle <= 0.50 {
		return &SavingsOpportunity{
			Type:       "node_consolidation",
			Applicable: false,
			Reason:     "No node is idle enough to consolidate",
		}
	}

	// Check if remaining nodes can absorb the workload
	removedUsed := 1.0 - mostIdleNode.IdlePercent
	var remainingIdle float64
	for _, node := range report.Nodes {
		if node.Name == mostIdleNode.Name {
			continue
		}
		remainingIdle += node.IdlePercent
	}
	remainingCount := float64(len(report.Nodes) - 1)
	avgRemainingIdle := remainingIdle / remainingCount

	// After redistribution, remaining nodes must stay below 80% utilized
	newUtilization := (1 - avgRemainingIdle) + (removedUsed / remainingCount)
	if newUtilization > 0.80 {
		return &SavingsOpportunity{
			Type:       "node_consolidation",
			Applicable: false,
			Reason:     "Remaining nodes cannot safely absorb workload",
		}
	}

	return &SavingsOpportunity{
		Type:           "node_consolidation",
		MonthlySavings: mostIdleNode.MonthlyPrice,
		Applicable:     true,
		Reason:         "Remove most idle node and redistribute workloads",
		AffectedNodes:  []string{mostIdleNode.Name},
	}
}

func calculateRightSizingSavings(report *analyzer.CostReport) *SavingsOpportunity {
	var lowUtilNodes []string
	var totalSavings float64

	for _, node := range report.Nodes {
		if node.CPURequested < 0.40 && node.MemRequested < 0.40 {
			lowUtilNodes = append(lowUtilNodes, node.Name)
			totalSavings += node.MonthlyPrice * 0.50
		}
	}

	if len(lowUtilNodes) == 0 {
		return &SavingsOpportunity{
			Type:       "right_sizing",
			Applicable: false,
			Reason:     "Resource utilization is appropriate for current instance sizes",
		}
	}

	return &SavingsOpportunity{
		Type:           "right_sizing",
		MonthlySavings: totalSavings,
		Applicable:     true,
		Reason:         "Downsize instances with low CPU and memory utilization",
		AffectedNodes:  lowUtilNodes,
	}
}

// TotalSavings returns the max of applicable strategies (they overlap, so summing would be wrong).
func (p *PotentialSavings) TotalSavings() float64 {
	var max float64
	if p.SpotConversion != nil && p.SpotConversion.Applicable {
		if p.SpotConversion.MonthlySavings > max {
			max = p.SpotConversion.MonthlySavings
		}
	}
	if p.NodeConsolidation != nil && p.NodeConsolidation.Applicable {
		if p.NodeConsolidation.MonthlySavings > max {
			max = p.NodeConsolidation.MonthlySavings
		}
	}
	if p.RightSizing != nil && p.RightSizing.Applicable {
		if p.RightSizing.MonthlySavings > max {
			max = p.RightSizing.MonthlySavings
		}
	}
	return max
}

