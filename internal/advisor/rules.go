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
	var avgIdlePercent float64
	nodeCount := 0

	for _, node := range report.Nodes {
		if !node.IsSpot {
			onDemandCost += node.MonthlyPrice
			onDemandNodes = append(onDemandNodes, node.Name)
			avgIdlePercent += node.IdlePercent
			nodeCount++
		}
	}

	if nodeCount == 0 {
		return &SavingsOpportunity{
			Type:       "spot_conversion",
			Applicable: false,
			Reason:     "All nodes are already spot instances",
		}
	}

	avgIdlePercent = avgIdlePercent / float64(nodeCount)

	// Spot typically saves 60-70%, we use 70% conservatively
	spotDiscount := 0.70
	monthlySavings := onDemandCost * spotDiscount

	return &SavingsOpportunity{
		Type:           "spot_conversion",
		MonthlySavings: monthlySavings,
		Applicable:     avgIdlePercent > 0.20, // Only recommend if idle > 20% (utilization < 80%)
		Reason:         "Convert on-demand instances to spot for 70% savings",
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

	// Find the most idle node (highest IdlePercent)
	var mostIdleNode *analyzer.NodeCost
	var highestIdle float64 = 0.0

	for i := range report.Nodes {
		node := &report.Nodes[i]
		if node.IdlePercent > highestIdle {
			highestIdle = node.IdlePercent
			mostIdleNode = node
		}
	}

	// Can consolidate if:
	// 1. Most idle node has > 50% idle (was: utilization < 50%)
	// 2. Average cluster idle is > 30% (was: utilization < 70%)
	var totalIdle float64
	for _, node := range report.Nodes {
		totalIdle += node.IdlePercent
	}
	avgClusterIdle := totalIdle / float64(len(report.Nodes))

	if highestIdle <= 0.50 || avgClusterIdle <= 0.30 {
		return &SavingsOpportunity{
			Type:       "node_consolidation",
			Applicable: false,
			Reason:     "Utilization too high to consolidate safely",
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
	// Check if memory requested is consistently low (< 40%)
	// This suggests nodes could be downsized
	var lowMemNodes []string
	var totalSavings float64

	for _, node := range report.Nodes {
		if node.MemRequested < 0.40 {
			lowMemNodes = append(lowMemNodes, node.Name)
			// Downsizing typically saves ~50% (e.g., t3.large -> t3.medium)
			totalSavings += node.MonthlyPrice * 0.50
		}
	}

	if len(lowMemNodes) == 0 {
		return &SavingsOpportunity{
			Type:       "right_sizing",
			Applicable: false,
			Reason:     "Memory utilization is appropriate for current instance sizes",
		}
	}

	return &SavingsOpportunity{
		Type:           "right_sizing",
		MonthlySavings: totalSavings,
		Applicable:     true,
		Reason:         "Downsize instances with low memory utilization",
		AffectedNodes:  lowMemNodes,
	}
}

// TotalSavings returns the sum of all applicable savings
func (p *PotentialSavings) TotalSavings() float64 {
	var total float64
	if p.SpotConversion != nil && p.SpotConversion.Applicable {
		total += p.SpotConversion.MonthlySavings
	}
	if p.NodeConsolidation != nil && p.NodeConsolidation.Applicable {
		total += p.NodeConsolidation.MonthlySavings
	}
	if p.RightSizing != nil && p.RightSizing.Applicable {
		total += p.RightSizing.MonthlySavings
	}
	return total
}
