package advisor

import (
	"fmt"

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

// SavingsConfig holds configurable parameters for savings calculations.
type SavingsConfig struct {
	SpotDiscountRate float64 // 0.0-1.0, default 0.79
}

// DefaultSavingsConfig returns default savings parameters.
func DefaultSavingsConfig() SavingsConfig {
	return SavingsConfig{
		SpotDiscountRate: 0.79,
	}
}

// CalculateSavings computes deterministic savings opportunities from the cost report.
func CalculateSavings(report *analyzer.CostReport, cfg SavingsConfig) *PotentialSavings {
	if cfg.SpotDiscountRate == 0 {
		cfg.SpotDiscountRate = 0.79
	}

	savings := &PotentialSavings{}

	// 1. Spot conversion savings
	savings.SpotConversion = calculateSpotSavings(report, cfg.SpotDiscountRate)

	// 2. Node consolidation: remove underutilized nodes
	savings.NodeConsolidation = calculateConsolidationSavings(report)

	// 3. Right-sizing: pod-level with p95, fallback to node-level
	savings.RightSizing = calculateRightSizingSavings(report)

	return savings
}

func calculateSpotSavings(report *analyzer.CostReport, discountRate float64) *SavingsOpportunity {
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

	monthlySavings := onDemandCost * discountRate

	return &SavingsOpportunity{
		Type:           "spot_conversion",
		MonthlySavings: monthlySavings,
		Applicable:     true,
		Reason:         fmt.Sprintf("Max potential savings if stateless workloads moved to spot (%.0f%% discount)", discountRate*100),
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
	// Pod-level rightsizing: if pod has separate CPU/RAM cost data, use it
	if report.MetricsSource == "prometheus" && len(report.AllPods) > 0 {
		return calculatePodRightSizingSavings(report)
	}

	// Node-level fallback when no Prometheus data
	return calculateNodeRightSizingSavings(report)
}

func calculatePodRightSizingSavings(report *analyzer.CostReport) *SavingsOpportunity {
	var totalSavings float64
	var affectedPods []string

	for _, pod := range report.AllPods {
		if pod.CPURequest == 0 && pod.MemRequest == 0 {
			continue
		}

		var cpuSavings, ramSavings float64

		// Use P95 usage for safer recommendations, fall back to avg usage
		cpuUsage := pod.CPUUsage
		if pod.CPUP95Usage > 0 {
			cpuUsage = pod.CPUP95Usage
		}
		memUsage := pod.MemUsage
		if pod.MemoryP95Usage > 0 {
			memUsage = pod.MemoryP95Usage
		}

		// CPU: if efficiency < 50% and pod actually has some usage, recommend downsizing
		if pod.CPUEfficiency > 0 && pod.CPUEfficiency < 0.50 && pod.CPUCost > 0 {
			// Recommend request = p95_usage * 1.2 (20% headroom)
			recommended := cpuUsage * 1.2 * 1000 // to millicores
			if recommended < float64(pod.CPURequest) {
				cpuSavings = pod.CPUCost * (1.0 - recommended/float64(pod.CPURequest))
			}
		}

		// RAM: if efficiency < 50% and pod actually has some usage, recommend downsizing
		if pod.MemEfficiency > 0 && pod.MemEfficiency < 0.50 && pod.RAMCost > 0 {
			recommended := float64(memUsage) * 1.2
			if recommended < float64(pod.MemRequest) {
				ramSavings = pod.RAMCost * (1.0 - recommended/float64(pod.MemRequest))
			}
		}

		podSavings := cpuSavings + ramSavings
		if podSavings > 1.0 { // ignore trivial savings < $1/mo
			totalSavings += podSavings
			affectedPods = append(affectedPods, pod.Namespace+"/"+pod.Name)
		}
	}

	if len(affectedPods) == 0 {
		return &SavingsOpportunity{
			Type:       "right_sizing",
			Applicable: false,
			Reason:     "Pod resource utilization is appropriate for current requests",
		}
	}

	return &SavingsOpportunity{
		Type:           "right_sizing",
		MonthlySavings: totalSavings,
		Applicable:     true,
		Reason:         fmt.Sprintf("Downsize %d over-provisioned pods (usage-based recommendations)", len(affectedPods)),
		AffectedNodes:  affectedPods,
	}
}

func calculateNodeRightSizingSavings(report *analyzer.CostReport) *SavingsOpportunity {
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

