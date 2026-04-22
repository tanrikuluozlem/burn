package analyzer

import (
	"context"
	"log/slog"
	"sort"
	"time"

	"github.com/tanrikuluozlem/burn/internal/collector"
	"github.com/tanrikuluozlem/burn/internal/pricing"
)

const (
	hoursPerMonth    = 730 // average hours in a month
	highIdlePercent  = 0.7 // 70% idle considered wasteful
	maxPodEfficiency = 10  // show top N inefficient pods
)

type Analyzer struct {
	pricing pricing.Provider
}

func New(p pricing.Provider) *Analyzer {
	return &Analyzer{pricing: p}
}

func (a *Analyzer) Analyze(ctx context.Context, info *collector.ClusterInfo) (*CostReport, error) {
	hasPrometheus := hasPrometheusMetrics(info.Nodes)

	report := &CostReport{
		GeneratedAt:   time.Now().UTC(),
		TotalNodes:    info.TotalNodes,
		TotalPods:     info.TotalPods,
		Nodes:         make([]NodeCost, 0, len(info.Nodes)),
		MetricsSource: "requests",
	}
	if hasPrometheus {
		report.MetricsSource = "prometheus"
	}

	var totalHourly float64
	var totalIdleHourly float64
	var skipped int
	var allPods []PodEfficiency

	for _, node := range info.Nodes {
		nc, pods, err := a.calculateNodeCost(ctx, node, hasPrometheus)
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
		totalIdleHourly += nc.IdleCostHourly
		report.Nodes = append(report.Nodes, nc)
		allPods = append(allPods, pods...)

		if nc.IdlePercent >= highIdlePercent {
			report.WasteAnalysis.UnderutilizedNodes = append(
				report.WasteAnalysis.UnderutilizedNodes,
				UnderutilizedNode{
					Name:           nc.Name,
					IdlePercent:    nc.IdlePercent,
					IdleCost:       nc.IdleCostMonthly,
					Recommendation: recommendationFor(nc),
				},
			)
			report.WasteAnalysis.PotentialSavings += nc.IdleCostMonthly * 0.7
		}
	}

	report.HourlyCost = totalHourly
	report.MonthlyCost = totalHourly * hoursPerMonth
	report.TotalIdleCost = totalIdleHourly * hoursPerMonth
	report.SkippedNodes = skipped

	// Sort pods by efficiency (lowest first) and take top N
	if hasPrometheus && len(allPods) > 0 {
		sort.Slice(allPods, func(i, j int) bool {
			return allPods[i].CPUEfficiency < allPods[j].CPUEfficiency
		})
		if len(allPods) > maxPodEfficiency {
			allPods = allPods[:maxPodEfficiency]
		}
		report.InefficientPods = allPods
	}

	return report, nil
}

func hasPrometheusMetrics(nodes []collector.NodeInfo) bool {
	for _, node := range nodes {
		if node.CPUUsage > 0 || node.MemoryUsage > 0 {
			return true
		}
	}
	return false
}

func (a *Analyzer) calculateNodeCost(ctx context.Context, node collector.NodeInfo, hasPrometheus bool) (NodeCost, []PodEfficiency, error) {
	price, err := a.pricing.GetHourlyPriceForNode(ctx, node)
	if err != nil {
		return NodeCost{}, nil, err
	}

	// Calculate resource requests (what pods asked for as % of node capacity)
	cpuRequested := resourcePercentage(sumPodCPU(node.Pods), node.CPUAllocatable)
	memRequested := resourcePercentage(sumPodMemory(node.Pods), node.MemAllocatable)

	// Calculate idle capacity (unused portion of node)
	// Idle = 1 - requested (based on scheduling view)
	// If Prometheus available, use actual usage for more accurate idle
	var usedPercent float64
	if hasPrometheus && (node.CPUUsage > 0 || node.MemoryUsage > 0) {
		cpuUsed := node.CPUUsage / (float64(node.CPUAllocatable) / 1000.0)
		memUsed := float64(node.MemoryUsage) / float64(node.MemAllocatable)
		usedPercent = (cpuUsed + memUsed) / 2
	} else {
		usedPercent = (cpuRequested + memRequested) / 2
	}

	idlePercent := 1.0 - usedPercent
	if idlePercent < 0 {
		idlePercent = 0
	}
	idleCostHourly := price * idlePercent

	// Calculate pod efficiency (only if Prometheus available)
	var podEfficiencies []PodEfficiency
	if hasPrometheus {
		podEfficiencies = calculatePodEfficiencies(node.Pods, price, node.CPUAllocatable, node.MemAllocatable)
	}

	return NodeCost{
		Name:            node.Name,
		InstanceType:    node.InstanceType,
		Region:          node.Region,
		IsSpot:          node.IsSpot,
		HourlyPrice:     price,
		MonthlyPrice:    price * hoursPerMonth,
		PodCount:        len(node.Pods),
		CPURequested:    cpuRequested,
		MemRequested:    memRequested,
		IdleCostHourly:  idleCostHourly,
		IdleCostMonthly: idleCostHourly * hoursPerMonth,
		IdlePercent:     idlePercent,
	}, podEfficiencies, nil
}

func calculatePodEfficiencies(pods []collector.PodInfo, nodeHourlyPrice float64, nodeCPUAllocatable, nodeMemAllocatable int64) []PodEfficiency {
	var result []PodEfficiency

	for _, pod := range pods {
		// Skip pods without requests (can't calculate efficiency)
		if pod.CPURequest == 0 && pod.MemoryRequest == 0 {
			continue
		}

		// CPU efficiency: usage (cores) / request (millicores converted to cores)
		var cpuEff float64
		if pod.CPURequest > 0 {
			cpuRequestCores := float64(pod.CPURequest) / 1000.0
			cpuEff = pod.CPUUsage / cpuRequestCores
		}

		// Memory efficiency: usage / request
		var memEff float64
		if pod.MemoryRequest > 0 {
			memEff = float64(pod.MemoryUsage) / float64(pod.MemoryRequest)
		}

		// Estimate pod cost based on its share of node resources
		cpuShare := float64(pod.CPURequest) / float64(nodeCPUAllocatable)
		memShare := float64(pod.MemoryRequest) / float64(nodeMemAllocatable)
		podHourlyCost := nodeHourlyPrice * (cpuShare + memShare) / 2

		result = append(result, PodEfficiency{
			Name:          pod.Name,
			Namespace:     pod.Namespace,
			CPURequest:    pod.CPURequest,
			CPUUsage:      pod.CPUUsage,
			CPUEfficiency: cpuEff,
			MemRequest:    pod.MemoryRequest,
			MemUsage:      pod.MemoryUsage,
			MemEfficiency: memEff,
			MonthlyCost:   podHourlyCost * hoursPerMonth,
		})
	}

	return result
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
	if nc.IdlePercent > 0.9 {
		return "Very high idle (>90%) - consider smaller instance type"
	}
	if !nc.IsSpot && nc.IdlePercent > 0.7 {
		return "High idle on on-demand - consider spot instances"
	}
	return "Review workload placement"
}
