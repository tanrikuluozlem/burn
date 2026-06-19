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
	hoursPerMonth    = 730.0
	highIdlePercent  = 0.4
	maxPodEfficiency = 10
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
			if !nc.IsSpot {
				report.WasteAnalysis.PotentialSavings += nc.MonthlyPrice
			}
		}
	}

	report.HourlyCost = totalHourly
	report.MonthlyCost = totalHourly * hoursPerMonth
	report.TotalIdleCost = totalIdleHourly * hoursPerMonth
	report.SkippedNodes = skipped
	report.AllPods = allPods

	pvCosts := calculatePVCosts(ctx, info.PVCs, a.pricing)
	report.PVCosts = pvCosts
	for _, pv := range pvCosts {
		report.TotalPVCost += pv.MonthlyCost
	}

	lbCosts := calculateLBCosts(info.LoadBalancers, a.pricing)
	report.LBCosts = lbCosts
	for _, lb := range lbCosts {
		report.TotalLBCost += lb.MonthlyCost
	}

	// TODO: network cost needs traffic classification (zone/region/internet)
	report.NetworkCost = NetworkCost{}
	report.TotalNetworkCost = 0

	report.Namespaces = aggregateByNamespace(allPods)
	addStorageCostToNamespaces(report.Namespaces, pvCosts)

	report.TotalMonthlyCost = report.MonthlyCost + report.TotalPVCost + report.TotalLBCost + report.TotalNetworkCost

	hasCloud := false
	for _, n := range info.Nodes {
		if n.CloudProvider != collector.CloudUnknown {
			hasCloud = true
			break
		}
	}

	if len(info.Workloads) > 0 && hasCloud {
		nsCosts := make(map[string]float64)
		for _, ns := range report.Namespaces {
			if ns.PodCount > 0 {
				nsCosts[ns.Name] = (ns.MonthlyCost - ns.StorageCost) / float64(ns.PodCount)
			}
		}
		for i := range info.Workloads {
			perPod := nsCosts[info.Workloads[i].Namespace]
			info.Workloads[i].MonthlyCost = perPod * float64(info.Workloads[i].Replicas)
		}
		report.SpotReadiness = CheckSpotReadiness(info.Workloads)

		// assumes homogeneous cluster for spot discount lookup
		if len(info.Nodes) > 0 {
			sd := a.pricing.GetSpotDiscount(ctx, info.Nodes[0].InstanceType, info.Nodes[0].Region)
			for i := range report.SpotReadiness {
				if report.SpotReadiness[i].Status == "spot-ready" {
					report.SpotReadiness[i].Discount = sd.Discount
					report.SpotReadiness[i].InterruptionRate = sd.InterruptionRate
					report.SpotReadiness[i].PricingSource = sd.Source
				}
			}
		}
		report.SpotSavings = SpotSavings(report.SpotReadiness)

		// apply real spot discount to waste analysis
		if report.WasteAnalysis.PotentialSavings > 0 {
			for _, s := range report.SpotReadiness {
				if s.Status == "spot-ready" && s.Discount > 0 {
					report.WasteAnalysis.PotentialSavings *= s.Discount
					break
				}
			}
		}
	}

	if hasPrometheus && len(allPods) > 0 {
		var validPods []PodEfficiency
		for _, p := range allPods {
			if p.CPUEfficiency >= 0 {
				validPods = append(validPods, p)
			}
		}
		sorted := make([]PodEfficiency, len(validPods))
		copy(sorted, validPods)
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
	if node.CPUAllocatable <= 0 || node.MemAllocatable <= 0 {
		return NodeCost{Name: node.Name, InstanceType: node.InstanceType}, nil, nil
	}

	np, err := a.pricing.GetNodePricing(ctx, node)
	if err != nil {
		return NodeCost{}, nil, err
	}
	if np == nil {
		return NodeCost{Name: node.Name, InstanceType: node.InstanceType}, nil, nil
	}

	cpuRequested := resourcePercentage(sumPodCPU(node.Pods), node.CPUAllocatable)
	memRequested := resourcePercentage(sumPodMemory(node.Pods), node.MemAllocatable)

	podEfficiencies := calculatePodEfficiencies(node.Pods, np, hasPrometheus)

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
		GPUCostPerUnit:  np.GPUCostPerUnit,
		GPUCount:        node.GPUCount,
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
		if pod.CPURequest == 0 && pod.MemoryRequest == 0 {
			continue
		}

		cpuEff := -1.0
		if pod.CPURequest > 0 {
			cpuEff = pod.CPUUsage / (float64(pod.CPURequest) / 1000.0)
		}

		memEff := -1.0
		if pod.MemoryRequest > 0 {
			memEff = float64(pod.MemoryUsage) / float64(pod.MemoryRequest)
		}

		cpuCores := float64(pod.CPURequest) / 1000.0
		if hasPrometheus && pod.CPUUsage > cpuCores {
			cpuCores = pod.CPUUsage
		}

		ramGiB := float64(pod.MemoryRequest) / (1024 * 1024 * 1024)
		usageGiB := float64(pod.MemoryUsage) / (1024 * 1024 * 1024)
		if hasPrometheus && usageGiB > ramGiB {
			ramGiB = usageGiB
		}

		cpuHourlyCost := cpuCores * np.CPUCostPerCore
		ramHourlyCost := ramGiB * np.RAMCostPerGiB
		gpuHourlyCost := float64(pod.GPURequest) * np.GPUCostPerUnit
		podHourlyCost := cpuHourlyCost + ramHourlyCost + gpuHourlyCost

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
			GPUCost:        gpuHourlyCost * hoursPerMonth,
			GPURequest:     pod.GPURequest,
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

func calculatePVCosts(ctx context.Context, pvcs []collector.PVCInfo, p pricing.Provider) []PVCost {
	var result []PVCost
	for _, pvc := range pvcs {
		gib := float64(pvc.RequestedBytes) / (1024 * 1024 * 1024)
		if gib <= 0 {
			continue
		}
		pricePerMonth := p.GetStoragePricePerGiBMonth(ctx, pvc.StorageClass)
		result = append(result, PVCost{
			Name:         pvc.Name,
			Namespace:    pvc.Namespace,
			StorageClass: pvc.StorageClass,
			CapacityGiB:  gib,
			MonthlyCost:  gib * pricePerMonth,
		})
	}
	return result
}

func calculateLBCosts(lbs []collector.LBServiceInfo, p pricing.Provider) []LBCost {
	var result []LBCost
	pricePerHour := p.GetLoadBalancerPricePerHour()
	for _, lb := range lbs {
		result = append(result, LBCost{
			Name:        lb.Name,
			Namespace:   lb.Namespace,
			MonthlyCost: pricePerHour * hoursPerMonth,
		})
	}
	return result
}

func addStorageCostToNamespaces(namespaces []NamespaceCost, pvCosts []PVCost) {
	nsMap := make(map[string]int)
	for i, ns := range namespaces {
		nsMap[ns.Name] = i
	}
	for _, pv := range pvCosts {
		if idx, ok := nsMap[pv.Namespace]; ok {
			namespaces[idx].StorageCost += pv.MonthlyCost
			namespaces[idx].MonthlyCost += pv.MonthlyCost
		}
	}
}
