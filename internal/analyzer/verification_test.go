package analyzer

import (
	"context"
	"math"
	"testing"

	"github.com/tanrikuluozlem/burn/internal/collector"
	"github.com/tanrikuluozlem/burn/internal/pricing"
)

// m5.xlarge scenario: pod costs + idle must sum to node cost.
func TestCostAllocationFormula(t *testing.T) {
	const tol = 0.01

	a := New(&mockPricing{price: 0.192})

	info := &collector.ClusterInfo{
		TotalNodes: 1,
		TotalPods:  2,
		Nodes: []collector.NodeInfo{{
			Name:           "ip-10-0-1-5",
			InstanceType:   "m5.xlarge",
			Region:         "us-east-1",
			CPUAllocatable: 4000,                    // 4 cores
			MemAllocatable: 16 * 1024 * 1024 * 1024, // 16 GiB
			Pods: []collector.PodInfo{
				{
					Name:          "web-abc123",
					Namespace:     "prod",
					CPURequest:    1000,                    // 1 core
					MemoryRequest: 4 * 1024 * 1024 * 1024, // 4 GiB
				},
				{
					Name:          "api-def456",
					Namespace:     "prod",
					CPURequest:    2000,                    // 2 cores
					MemoryRequest: 8 * 1024 * 1024 * 1024, // 8 GiB
				},
			},
		}},
	}

	report, err := a.Analyze(context.Background(), info)
	if err != nil {
		t.Fatal(err)
	}

	// 1. Total cluster cost must equal node price * 730
	expectedMonthlyCost := 0.192 * 730
	if math.Abs(report.MonthlyCost-expectedMonthlyCost) > tol {
		t.Errorf("Cluster MonthlyCost = $%.2f, expected $%.2f", report.MonthlyCost, expectedMonthlyCost)
	}

	// 2. Verify cost split ratios
	cpuPerCore, ramPerGiB := pricing.SplitNodeCost(0.192, 4000, 16*1024*1024*1024)

	// cpuPerCore should be ~7.46x ramPerGiB
	ratio := cpuPerCore / ramPerGiB
	if math.Abs(ratio-pricing.CPUToRAMRatio) > 0.01 {
		t.Errorf("CPU/RAM ratio = %.4f, expected %.4f", ratio, pricing.CPUToRAMRatio)
	}

	// Sum must equal node price
	reconstituted := cpuPerCore*4 + ramPerGiB*16
	if math.Abs(reconstituted-0.192) > 0.001 {
		t.Errorf("Reconstituted price = $%.6f, expected $0.192", reconstituted)
	}

	// 3. Pod costs
	if len(report.AllPods) != 2 {
		t.Fatalf("Expected 2 pods, got %d", len(report.AllPods))
	}

	var webPod, apiPod PodEfficiency
	for _, p := range report.AllPods {
		if p.Name == "web-abc123" {
			webPod = p
		} else {
			apiPod = p
		}
	}

	// web: 1 core, 4 GiB
	expectedWebCPU := cpuPerCore * 1 * 730
	expectedWebRAM := ramPerGiB * 4 * 730
	if math.Abs(webPod.CPUCost-expectedWebCPU) > tol {
		t.Errorf("web CPUCost = $%.2f, expected $%.2f", webPod.CPUCost, expectedWebCPU)
	}
	if math.Abs(webPod.RAMCost-expectedWebRAM) > tol {
		t.Errorf("web RAMCost = $%.2f, expected $%.2f", webPod.RAMCost, expectedWebRAM)
	}
	if math.Abs(webPod.MonthlyCost-(webPod.CPUCost+webPod.RAMCost)) > tol {
		t.Errorf("web MonthlyCost ($%.2f) != CPUCost ($%.2f) + RAMCost ($%.2f)",
			webPod.MonthlyCost, webPod.CPUCost, webPod.RAMCost)
	}

	// api: 2 cores, 8 GiB — should be exactly 2x the web pod
	if math.Abs(apiPod.MonthlyCost-webPod.MonthlyCost*2) > tol {
		t.Errorf("api cost ($%.2f) should be 2x web cost ($%.2f)", apiPod.MonthlyCost, webPod.MonthlyCost)
	}

	// 4. Sum of all pod costs + idle = total node cost
	totalPodCost := webPod.MonthlyCost + apiPod.MonthlyCost
	totalIdleCost := report.TotalIdleCost
	totalAccountedFor := totalPodCost + totalIdleCost

	if math.Abs(totalAccountedFor-expectedMonthlyCost) > tol {
		t.Errorf("Pod costs ($%.2f) + Idle ($%.2f) = $%.2f, expected $%.2f (node cost)",
			totalPodCost, totalIdleCost, totalAccountedFor, expectedMonthlyCost)
	}

	// 5. Idle should reflect unallocated resources (1 core, 4 GiB unallocated)
	expectedCPUIdle := cpuPerCore * 1 * 730 // 1 unallocated core
	expectedRAMIdle := ramPerGiB * 4 * 730  // 4 unallocated GiB
	node := report.Nodes[0]

	if math.Abs(node.CPUIdleCost-expectedCPUIdle) > tol {
		t.Errorf("CPUIdleCost = $%.2f, expected $%.2f", node.CPUIdleCost, expectedCPUIdle)
	}
	if math.Abs(node.RAMIdleCost-expectedRAMIdle) > tol {
		t.Errorf("RAMIdleCost = $%.2f, expected $%.2f", node.RAMIdleCost, expectedRAMIdle)
	}

	// 6. Namespace aggregation
	if len(report.Namespaces) != 1 {
		t.Fatalf("Expected 1 namespace, got %d", len(report.Namespaces))
	}
	ns := report.Namespaces[0]
	if ns.Name != "prod" {
		t.Errorf("Namespace name = %s, expected prod", ns.Name)
	}
	if math.Abs(ns.MonthlyCost-totalPodCost) > tol {
		t.Errorf("Namespace cost ($%.2f) != sum of pod costs ($%.2f)", ns.MonthlyCost, totalPodCost)
	}
	if math.Abs(ns.MonthlyCost-(ns.CPUCost+ns.RAMCost)) > tol {
		t.Errorf("Namespace MonthlyCost ($%.2f) != CPUCost ($%.2f) + RAMCost ($%.2f)",
			ns.MonthlyCost, ns.CPUCost, ns.RAMCost)
	}
}

func TestBurstCostAllocation(t *testing.T) {
	const tol = 0.01

	a := New(&mockPricing{price: 0.192})

	info := &collector.ClusterInfo{
		TotalNodes: 1,
		TotalPods:  1,
		Nodes: []collector.NodeInfo{{
			Name:           "ip-10-0-1-5",
			InstanceType:   "m5.xlarge",
			Region:         "us-east-1",
			CPUAllocatable: 4000,
			MemAllocatable: 16 * 1024 * 1024 * 1024,
			CPUUsage:       3.5,
			MemoryUsage:    14 * 1024 * 1024 * 1024,
			Pods: []collector.PodInfo{
				{
					Name:          "burst-app",
					Namespace:     "prod",
					CPURequest:    1000,                    // requests 1 core
					MemoryRequest: 4 * 1024 * 1024 * 1024, // requests 4 GiB
					CPUUsage:      3.0,                     // actually uses 3 cores (burst!)
					MemoryUsage:   12 * 1024 * 1024 * 1024, // actually uses 12 GiB (burst!)
				},
			},
		}},
	}

	report, err := a.Analyze(context.Background(), info)
	if err != nil {
		t.Fatal(err)
	}

	pod := report.AllPods[0]
	cpuPerCore, ramPerGiB := pricing.SplitNodeCost(0.192, 4000, 16*1024*1024*1024)

	// max(request=1, usage=3) = 3 cores for CPU cost
	expectedCPUCost := 3.0 * cpuPerCore * 730
	if math.Abs(pod.CPUCost-expectedCPUCost) > tol {
		t.Errorf("Burst pod CPUCost = $%.2f, expected $%.2f (based on 3 cores usage, not 1 core request)",
			pod.CPUCost, expectedCPUCost)
	}

	// max(request=4GiB, usage=12GiB) = 12 GiB for RAM cost
	expectedRAMCost := 12.0 * ramPerGiB * 730
	if math.Abs(pod.RAMCost-expectedRAMCost) > tol {
		t.Errorf("Burst pod RAMCost = $%.2f, expected $%.2f (based on 12 GiB usage, not 4 GiB request)",
			pod.RAMCost, expectedRAMCost)
	}

	// Total should equal CPUCost + RAMCost
	if math.Abs(pod.MonthlyCost-(pod.CPUCost+pod.RAMCost)) > tol {
		t.Errorf("MonthlyCost ($%.2f) != CPUCost ($%.2f) + RAMCost ($%.2f)",
			pod.MonthlyCost, pod.CPUCost, pod.RAMCost)
	}
}

func TestMultiNodeMultiNamespace(t *testing.T) {
	const tol = 0.05

	a := New(&mockPricing{price: 0.192})

	info := &collector.ClusterInfo{
		TotalNodes: 2,
		TotalPods:  4,
		Nodes: []collector.NodeInfo{
			{
				Name:           "node-1",
				InstanceType:   "m5.xlarge",
				Region:         "us-east-1",
				CPUAllocatable: 4000,
				MemAllocatable: 16 * 1024 * 1024 * 1024,
				Pods: []collector.PodInfo{
					{Name: "web-1", Namespace: "prod", CPURequest: 1000, MemoryRequest: 4 * 1024 * 1024 * 1024},
					{Name: "web-2", Namespace: "prod", CPURequest: 1000, MemoryRequest: 4 * 1024 * 1024 * 1024},
				},
			},
			{
				Name:           "node-2",
				InstanceType:   "m5.xlarge",
				Region:         "us-east-1",
				CPUAllocatable: 4000,
				MemAllocatable: 16 * 1024 * 1024 * 1024,
				Pods: []collector.PodInfo{
					{Name: "api-1", Namespace: "prod", CPURequest: 2000, MemoryRequest: 8 * 1024 * 1024 * 1024},
					{Name: "worker-1", Namespace: "batch", CPURequest: 1000, MemoryRequest: 2 * 1024 * 1024 * 1024},
				},
			},
		},
	}

	report, err := a.Analyze(context.Background(), info)
	if err != nil {
		t.Fatal(err)
	}

	// Total cluster cost = 2 nodes × $0.192 × 730 = $280.32
	expectedTotal := 2 * 0.192 * 730
	if math.Abs(report.MonthlyCost-expectedTotal) > tol {
		t.Errorf("Cluster MonthlyCost = $%.2f, expected $%.2f", report.MonthlyCost, expectedTotal)
	}

	// Sum of all pod costs + all idle = total cluster cost
	var totalPodCost float64
	for _, pod := range report.AllPods {
		totalPodCost += pod.MonthlyCost

		// Every pod: MonthlyCost = CPUCost + RAMCost
		if math.Abs(pod.MonthlyCost-(pod.CPUCost+pod.RAMCost)) > 0.01 {
			t.Errorf("Pod %s: MonthlyCost ($%.2f) != CPU ($%.2f) + RAM ($%.2f)",
				pod.Name, pod.MonthlyCost, pod.CPUCost, pod.RAMCost)
		}
	}

	if math.Abs((totalPodCost+report.TotalIdleCost)-expectedTotal) > tol {
		t.Errorf("Pods ($%.2f) + Idle ($%.2f) = $%.2f, expected $%.2f",
			totalPodCost, report.TotalIdleCost, totalPodCost+report.TotalIdleCost, expectedTotal)
	}

	// Namespace check
	if len(report.Namespaces) != 2 {
		t.Fatalf("Expected 2 namespaces, got %d", len(report.Namespaces))
	}

	var prodNS, batchNS NamespaceCost
	for _, ns := range report.Namespaces {
		if ns.Name == "prod" {
			prodNS = ns
		} else {
			batchNS = ns
		}
	}

	// Prod has 3 pods (web-1, web-2, api-1), batch has 1 (worker-1)
	if prodNS.PodCount != 3 {
		t.Errorf("prod PodCount = %d, expected 3", prodNS.PodCount)
	}
	if batchNS.PodCount != 1 {
		t.Errorf("batch PodCount = %d, expected 1", batchNS.PodCount)
	}

	// Prod should cost more than batch
	if prodNS.MonthlyCost <= batchNS.MonthlyCost {
		t.Errorf("prod cost ($%.2f) should be > batch cost ($%.2f)", prodNS.MonthlyCost, batchNS.MonthlyCost)
	}

	// Each namespace: MonthlyCost = CPUCost + RAMCost
	if math.Abs(prodNS.MonthlyCost-(prodNS.CPUCost+prodNS.RAMCost)) > tol {
		t.Errorf("prod: MonthlyCost != CPUCost + RAMCost")
	}
	if math.Abs(batchNS.MonthlyCost-(batchNS.CPUCost+batchNS.RAMCost)) > tol {
		t.Errorf("batch: MonthlyCost != CPUCost + RAMCost")
	}
}

func TestSpotNodePricing(t *testing.T) {
	const tol = 0.01

	spotPrice := 0.192 * 0.21
	a := New(&mockPricing{price: spotPrice})

	info := &collector.ClusterInfo{
		TotalNodes: 1,
		TotalPods:  1,
		Nodes: []collector.NodeInfo{{
			Name:           "spot-node-1",
			InstanceType:   "m5.xlarge",
			Region:         "us-east-1",
			IsSpot:         true,
			CPUAllocatable: 4000,
			MemAllocatable: 16 * 1024 * 1024 * 1024,
			Pods: []collector.PodInfo{
				{Name: "app", Namespace: "default", CPURequest: 2000, MemoryRequest: 8 * 1024 * 1024 * 1024},
			},
		}},
	}

	report, err := a.Analyze(context.Background(), info)
	if err != nil {
		t.Fatal(err)
	}

	// Spot monthly should be ~21% of on-demand monthly
	onDemandMonthly := 0.192 * 730
	spotMonthly := report.MonthlyCost
	ratio := spotMonthly / onDemandMonthly

	if math.Abs(ratio-0.21) > 0.01 {
		t.Errorf("Spot/OnDemand ratio = %.4f, expected ~0.21", ratio)
	}
}
