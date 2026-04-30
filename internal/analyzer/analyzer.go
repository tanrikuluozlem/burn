package analyzer

import (
	"context"
	"log/slog"
	"math"
	"sort"
	"time"

	"github.com/tanrikuluozlem/burn/internal/collector"
	"github.com/tanrikuluozlem/burn/internal/pricing"
)

const (
	hoursPerMonth    = 730 // average hours in a month
	highIdlePercent  = 0.4 // AWS Cost Explorer rightsizing threshold
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

		// Waste detection: flag nodes where idle cost exceeds threshold
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
			if !nc.IsSpot {
				report.WasteAnalysis.PotentialSavings += nc.MonthlyPrice * 0.79
			}
		}
	}

	report.HourlyCost = totalHourly
	report.MonthlyCost = totalHourly * hoursPerMonth
	report.TotalIdleCost = totalIdleHourly * hoursPerMonth
	report.SkippedNodes = skipped

	report.AllPods = allPods
	report.Namespaces = aggregateByNamespace(allPods)

	if hasPrometheus && len(allPods) > 0 {
		sorted := make([]PodEfficiency, len(allPods))
		copy(sorted, allPods)
		sort.Slice(sorted, func(i, j int) bool {
			return sorted[i].CPUEfficiency < sorted[j].CPUEfficiency
		})
		if len(sorted) > maxPodEfficiency {
			sorted = sorted[:maxPodEfficiency]
		}
		report.InefficientPods = sorted
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
	np, err := a.pricing.GetNodePricing(ctx, node)
	if err != nil {
		return NodeCost{}, nil, err
	}

	cpuRequested := resourcePercentage(sumPodCPU(node.Pods), node.CPUAllocatable)
	memRequested := resourcePercentage(sumPodMemory(node.Pods), node.MemAllocatable)

	podEfficiencies := calculatePodEfficiencies(node.Pods, np, hasPrometheus)

	// Per-resource idle
	var totalPodCPUHourly, totalPodRAMHourly float64
	for _, p := range podEfficiencies {
		totalPodCPUHourly += p.CPUCost / hoursPerMonth
		totalPodRAMHourly += p.RAMCost / hoursPerMonth
	}

	cpuCores := float64(node.CPUAllocatable) / 1000.0
	ramGiB := float64(node.MemAllocatable) / (1024 * 1024 * 1024)
	nodeCPUCostHourly := np.CPUCostPerCore * cpuCores
	nodeRAMCostHourly := np.RAMCostPerGiB * ramGiB

	cpuIdleHourly := math.Max(0, nodeCPUCostHourly-totalPodCPUHourly)
	ramIdleHourly := math.Max(0, nodeRAMCostHourly-totalPodRAMHourly)
	idleCostHourly := cpuIdleHourly + ramIdleHourly

	idlePercent := 0.0
	if np.HourlyTotal > 0 {
		idlePercent = idleCostHourly / np.HourlyTotal
	}

	return NodeCost{
		Name:            node.Name,
		InstanceType:    node.InstanceType,
		Region:          node.Region,
		IsSpot:          node.IsSpot,
		HourlyPrice:     np.HourlyTotal,
		MonthlyPrice:    np.HourlyTotal * hoursPerMonth,
		PodCount:        len(node.Pods),
		CPUCostPerCore:  np.CPUCostPerCore,
		RAMCostPerGiB:   np.RAMCostPerGiB,
		CPURequested:    cpuRequested,
		MemRequested:    memRequested,
		IdleCostHourly:  idleCostHourly,
		IdleCostMonthly: idleCostHourly * hoursPerMonth,
		IdlePercent:     idlePercent,
		CPUIdleCost:     cpuIdleHourly * hoursPerMonth,
		RAMIdleCost:     ramIdleHourly * hoursPerMonth,
	}, podEfficiencies, nil
}

func calculatePodEfficiencies(pods []collector.PodInfo, np *pricing.NodePricing, hasPrometheus bool) []PodEfficiency {
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

		// Effective resource: max(request, usage)
		cpuCores := float64(pod.CPURequest) / 1000.0
		if hasPrometheus && pod.CPUUsage > cpuCores {
			cpuCores = pod.CPUUsage
		}

		ramGiB := float64(pod.MemoryRequest) / (1024 * 1024 * 1024)
		usageGiB := float64(pod.MemoryUsage) / (1024 * 1024 * 1024)
		if hasPrometheus && usageGiB > ramGiB {
			ramGiB = usageGiB
		}

		// Per-resource cost
		cpuHourlyCost := cpuCores * np.CPUCostPerCore
		ramHourlyCost := ramGiB * np.RAMCostPerGiB
		podHourlyCost := cpuHourlyCost + ramHourlyCost

		result = append(result, PodEfficiency{
			Name:           pod.Name,
			Namespace:      pod.Namespace,
			CPURequest:     pod.CPURequest,
			CPUUsage:       pod.CPUUsage,
			CPUEfficiency:  cpuEff,
			MemRequest:     pod.MemoryRequest,
			MemUsage:       pod.MemoryUsage,
			MemEfficiency:  memEff,
			MonthlyCost:    podHourlyCost * hoursPerMonth,
			CPUCost:        cpuHourlyCost * hoursPerMonth,
			RAMCost:        ramHourlyCost * hoursPerMonth,
			CPUP95Usage:    pod.CPUP95Usage,
			MemoryP95Usage: pod.MemoryP95Usage,
		})
	}

	return result
}

func aggregateByNamespace(pods []PodEfficiency) []NamespaceCost {
	nsMap := make(map[string]*NamespaceCost)
	for _, p := range pods {
		ns, ok := nsMap[p.Namespace]
		if !ok {
			ns = &NamespaceCost{Name: p.Namespace}
			nsMap[p.Namespace] = ns
		}
		ns.PodCount++
		ns.CPURequest += p.CPURequest
		ns.CPUUsage += p.CPUUsage
		ns.MemRequest += p.MemRequest
		ns.MemUsage += p.MemUsage
		ns.MonthlyCost += p.MonthlyCost
		ns.CPUCost += p.CPUCost
		ns.RAMCost += p.RAMCost
	}

	result := make([]NamespaceCost, 0, len(nsMap))
	for _, ns := range nsMap {
		result = append(result, *ns)
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].MonthlyCost > result[j].MonthlyCost
	})
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
