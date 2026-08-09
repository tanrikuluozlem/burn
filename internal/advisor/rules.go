package advisor

import (
	"fmt"

	"github.com/tanrikuluozlem/burn/internal/analyzer"
)

type PotentialSavings struct {
	SpotConversion    *SavingsOpportunity
	NodeConsolidation *SavingsOpportunity
	RightSizing       *SavingsOpportunity
}

type SavingsOpportunity struct {
	Type           string
	MonthlySavings float64
	Applicable     bool
	Reason         string
	AffectedNodes  []string
}

type SavingsConfig struct{}

func DefaultSavingsConfig() SavingsConfig {
	return SavingsConfig{}
}

func CalculateSavings(report *analyzer.CostReport, cfg SavingsConfig) *PotentialSavings {
	savings := &PotentialSavings{}
	savings.SpotConversion = calculateSpotSavings(report)
	savings.NodeConsolidation = calculateConsolidationSavings(report)
	savings.RightSizing = calculateRightSizingSavings(report)

	return savings
}

func calculateSpotSavings(report *analyzer.CostReport) *SavingsOpportunity {
	if report.SpotSavings <= 0 {
		return &SavingsOpportunity{
			Type:       "spot_conversion",
			Applicable: false,
			Reason:     "No spot-ready workloads with pricing data",
		}
	}

	return &SavingsOpportunity{
		Type:           "spot_conversion",
		MonthlySavings: report.SpotSavings,
		Applicable:     true,
		Reason:         "Move spot-ready workloads to Spot instances",
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

		// CPU: recommend only when P95 was measured and is positive
		if pod.CPUP95Available && pod.CPUP95Usage > 0 &&
			pod.CPUEfficiency > 0 && pod.CPUEfficiency < 0.50 && pod.CPUCost > 0 {
			recommended := pod.CPUP95Usage * 1.5 * 1000 // p95 * 1.5 in millicores
			if recommended < float64(pod.CPURequest) {
				cpuSavings = pod.CPUCost * (1.0 - recommended/float64(pod.CPURequest))
			}
		}

		// Memory: recommend only when P95 was measured and is positive
		if pod.MemP95Available && pod.MemoryP95Usage > 0 &&
			pod.MemEfficiency > 0 && pod.MemEfficiency < 0.50 && pod.RAMCost > 0 {
			recommended := float64(pod.MemoryP95Usage) * 1.5
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
