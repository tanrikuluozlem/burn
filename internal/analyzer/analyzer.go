package analyzer

import (
	"context"
	"log/slog"
	"time"

	"github.com/tanrikuluozlem/burn/internal/collector"
	"github.com/tanrikuluozlem/burn/internal/pricing"
)

const (
	hoursPerMonth          = 730 // average hours in a month
	underutilizedThreshold = 0.3 // 30% utilization considered wasteful
)

type Analyzer struct {
	pricing pricing.Provider
}

func New(p pricing.Provider) *Analyzer {
	return &Analyzer{pricing: p}
}

func (a *Analyzer) Analyze(ctx context.Context, info *collector.ClusterInfo) (*CostReport, error) {
	report := &CostReport{
		GeneratedAt: time.Now().UTC(),
		TotalNodes:  info.TotalNodes,
		TotalPods:   info.TotalPods,
		Nodes:       make([]NodeCost, 0, len(info.Nodes)),
	}

	var totalHourly float64
	var skipped int

	for _, node := range info.Nodes {
		nc, err := a.calculateNodeCost(ctx, node)
		if err != nil {
			slog.Warn("failed to calculate node cost",
				"node", node.Name,
				"instance_type", node.InstanceType,
				"error", err,
			)
			skipped++
			continue
		}

		totalHourly += nc.HourlyPrice
		report.Nodes = append(report.Nodes, nc)

		if nc.Utilization < underutilizedThreshold {
			report.WasteAnalysis.UnderutilizedNodes = append(
				report.WasteAnalysis.UnderutilizedNodes,
				UnderutilizedNode{
					Name:           nc.Name,
					Utilization:    nc.Utilization,
					HourlyCost:     nc.HourlyPrice,
					Recommendation: recommendationFor(nc),
				},
			)
			report.WasteAnalysis.PotentialSavings += nc.HourlyPrice * 0.7 // could save ~70%
		}
	}

	report.HourlyCost = totalHourly
	report.MonthlyCost = totalHourly * hoursPerMonth
	report.SkippedNodes = skipped
	report.WasteAnalysis.PotentialSavings *= hoursPerMonth

	return report, nil
}

func (a *Analyzer) calculateNodeCost(ctx context.Context, node collector.NodeInfo) (NodeCost, error) {
	price, err := a.pricing.GetHourlyPrice(ctx, node.InstanceType, node.Region, node.IsSpot)
	if err != nil {
		return NodeCost{}, err
	}

	cpuPct := resourcePercentage(sumPodCPU(node.Pods), node.CPUAllocatable*1000)
	memPct := resourcePercentage(sumPodMemory(node.Pods), node.MemAllocatable)

	return NodeCost{
		Name:         node.Name,
		InstanceType: node.InstanceType,
		Region:       node.Region,
		IsSpot:       node.IsSpot,
		HourlyPrice:  price,
		MonthlyPrice: price * hoursPerMonth,
		PodCount:     len(node.Pods),
		CPURequested: cpuPct,
		MemRequested: memPct,
		Utilization:  (cpuPct + memPct) / 2,
	}, nil
}

func sumPodCPU(pods []collector.PodInfo) int64 {
	var total int64
	for _, p := range pods {
		total += p.CPURequest
	}
	return total
}

func sumPodMemory(pods []collector.PodInfo) int64 {
	var total int64
	for _, p := range pods {
		total += p.MemoryRequest
	}
	return total
}

func resourcePercentage(used, total int64) float64 {
	if total == 0 {
		return 0
	}
	return float64(used) / float64(total)
}

func recommendationFor(nc NodeCost) string {
	if nc.PodCount == 0 {
		return "Node has no pods - consider removing or cordoning"
	}
	if nc.Utilization < 0.1 {
		return "Very low utilization - consider smaller instance type"
	}
	if !nc.IsSpot && nc.Utilization < 0.3 {
		return "Low utilization on on-demand - consider spot instances"
	}
	return "Review workload placement"
}
