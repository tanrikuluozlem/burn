package analyzer

import (
	"context"
	"math"
	"testing"

	"github.com/tanrikuluozlem/burn/internal/collector"
	"github.com/tanrikuluozlem/burn/internal/pricing"
)

func floatEquals(a, b float64) bool {
	return math.Abs(a-b) < 0.0001
}

type mockPricing struct {
	price float64
}

func (m *mockPricing) GetHourlyPrice(context.Context, string, string, bool) (float64, error) {
	return m.price, nil
}

func (m *mockPricing) GetHourlyPriceForNode(_ context.Context, _ collector.NodeInfo) (float64, error) {
	return m.price, nil
}

func (m *mockPricing) GetNodePricing(_ context.Context, node collector.NodeInfo) (*pricing.NodePricing, error) {
	cpuPerCore, ramPerGiB := pricing.SplitNodeCost(m.price, node.CPUAllocatable, node.MemAllocatable)
	return &pricing.NodePricing{
		HourlyTotal:    m.price,
		CPUCostPerCore: cpuPerCore,
		RAMCostPerGiB:  ramPerGiB,
	}, nil
}

func (m *mockPricing) GetStoragePricePerGiBMonth(storageClass string) float64 { return 0.10 }
func (m *mockPricing) GetLoadBalancerPricePerHour() float64                    { return 0.0225 }
func (m *mockPricing) GetNetworkEgressPricePerGiB() float64                    { return 0.01 }

func TestResourcePercentage(t *testing.T) {
	if resourcePercentage(500, 1000) != 0.5 {
		t.Error("expected 0.5")
	}
	if resourcePercentage(0, 0) != 0 {
		t.Error("expected 0 for zero total")
	}
}

func TestSumPodCPU(t *testing.T) {
	pods := []collector.PodInfo{
		{CPURequest: 100},
		{CPURequest: 200},
	}
	if sumPodCPU(pods) != 300 {
		t.Error("expected 300")
	}
}

func TestRecommendationFor(t *testing.T) {
	nc := NodeCost{PodCount: 0}
	if recommendationFor(nc) != "Node has no pods - consider removing or cordoning" {
		t.Error("wrong recommendation for empty node")
	}

	nc = NodeCost{PodCount: 1, IdlePercent: 0.95}
	if recommendationFor(nc) != "Very high idle (>90%) - consider smaller instance type" {
		t.Error("wrong recommendation for very high idle")
	}

	nc = NodeCost{PodCount: 1, IdlePercent: 0.75, IsSpot: false}
	if recommendationFor(nc) != "High idle on on-demand - consider spot instances" {
		t.Error("wrong recommendation for high idle on-demand")
	}
}

func TestAnalyze(t *testing.T) {
	a := New(&mockPricing{price: 0.10})

	info := &collector.ClusterInfo{
		TotalNodes: 1,
		TotalPods:  2,
		Nodes: []collector.NodeInfo{{
			Name:           "node-1",
			InstanceType:   "t3.medium",
			Region:         "us-east-1",
			CPUAllocatable: 2000, // millicores
			MemAllocatable: 4 * 1024 * 1024 * 1024,
		}},
	}

	report, err := a.Analyze(context.Background(), info)
	if err != nil {
		t.Fatal(err)
	}
	if report.TotalNodes != 1 {
		t.Errorf("TotalNodes = %d", report.TotalNodes)
	}
	if report.HourlyCost != 0.10 {
		t.Errorf("HourlyCost = %v", report.HourlyCost)
	}
	if report.MetricsSource != "requests" {
		t.Errorf("MetricsSource = %s, expected requests", report.MetricsSource)
	}
}

func TestAnalyzeWithPrometheus(t *testing.T) {
	a := New(&mockPricing{price: 0.10})

	info := &collector.ClusterInfo{
		TotalNodes: 1,
		TotalPods:  1,
		Nodes: []collector.NodeInfo{{
			Name:           "node-1",
			InstanceType:   "t3.medium",
			Region:         "us-east-1",
			CPUAllocatable: 4000,                   // 4 cores in millicores
			MemAllocatable: 8 * 1024 * 1024 * 1024, // 8GB
			CPUUsage:       2.0,                     // 2 cores from Prometheus
			MemoryUsage:    4 * 1024 * 1024 * 1024,  // 4GB from Prometheus
			Pods: []collector.PodInfo{
				{
					Name:          "app",
					Namespace:     "default",
					CPURequest:    2000,                    // 2 cores
					MemoryRequest: 4 * 1024 * 1024 * 1024, // 4GB
					CPUUsage:      2.0,
					MemoryUsage:   4 * 1024 * 1024 * 1024,
				},
			},
		}},
	}

	report, err := a.Analyze(context.Background(), info)
	if err != nil {
		t.Fatal(err)
	}
	if report.MetricsSource != "prometheus" {
		t.Errorf("MetricsSource = %s, expected prometheus", report.MetricsSource)
	}

	node := report.Nodes[0]

	// Pod uses 50% CPU and 50% RAM → 50% idle
	if !floatEquals(node.IdlePercent, 0.5) {
		t.Errorf("IdlePercent = %v, expected ~0.5", node.IdlePercent)
	}
	if !floatEquals(node.IdleCostHourly, 0.05) {
		t.Errorf("IdleCostHourly = %v, expected ~0.05", node.IdleCostHourly)
	}
}

func TestAnalyzeIdleCost(t *testing.T) {
	a := New(&mockPricing{price: 0.10})

	// Scenario: Node with pods requesting 25% of capacity
	info := &collector.ClusterInfo{
		TotalNodes: 1,
		TotalPods:  1,
		Nodes: []collector.NodeInfo{{
			Name:           "node-1",
			InstanceType:   "t3.large",
			Region:         "us-east-1",
			CPUAllocatable: 2000,                   // 2 cores in millicores
			MemAllocatable: 8 * 1024 * 1024 * 1024, // 8GB
			Pods: []collector.PodInfo{
				{CPURequest: 500, MemoryRequest: 2 * 1024 * 1024 * 1024}, // requests 25% CPU, 25% MEM
			},
		}},
	}

	report, err := a.Analyze(context.Background(), info)
	if err != nil {
		t.Fatal(err)
	}

	node := report.Nodes[0]

	// Check requests
	expectedCPURequested := 0.25 // 500/2000
	if node.CPURequested != expectedCPURequested {
		t.Errorf("CPURequested = %v, expected %v", node.CPURequested, expectedCPURequested)
	}

	expectedMemRequested := 0.25 // 2GB/8GB
	if node.MemRequested != expectedMemRequested {
		t.Errorf("MemRequested = %v, expected %v", node.MemRequested, expectedMemRequested)
	}

	// Pod requests 25% of each resource → 75% idle
	if !floatEquals(node.IdlePercent, 0.75) {
		t.Errorf("IdlePercent = %v, expected ~0.75", node.IdlePercent)
	}

	if !floatEquals(node.IdleCostHourly, 0.075) {
		t.Errorf("IdleCostHourly = %v, expected ~0.075", node.IdleCostHourly)
	}
}

func TestPodEfficiency(t *testing.T) {
	a := New(&mockPricing{price: 0.10})

	// Scenario: Pods request resources but use much less
	info := &collector.ClusterInfo{
		TotalNodes: 1,
		TotalPods:  2,
		Nodes: []collector.NodeInfo{{
			Name:           "node-1",
			InstanceType:   "t3.large",
			Region:         "us-east-1",
			CPUAllocatable: 2000,                   // 2 cores
			MemAllocatable: 8 * 1024 * 1024 * 1024, // 8GB
			CPUUsage:       0.5,                    // 0.5 cores total
			MemoryUsage:    2 * 1024 * 1024 * 1024, // 2GB total
			Pods: []collector.PodInfo{
				{
					Name:          "nginx",
					Namespace:     "default",
					CPURequest:    1000,                    // 1 core
					MemoryRequest: 4 * 1024 * 1024 * 1024,  // 4GB
					CPUUsage:      0.1,                     // 0.1 cores actual
					MemoryUsage:   1 * 1024 * 1024 * 1024,  // 1GB actual
				},
				{
					Name:          "redis",
					Namespace:     "default",
					CPURequest:    500,                     // 0.5 core
					MemoryRequest: 2 * 1024 * 1024 * 1024,  // 2GB
					CPUUsage:      0.4,                     // 0.4 cores actual
					MemoryUsage:   1 * 1024 * 1024 * 1024,  // 1GB actual
				},
			},
		}},
	}

	report, err := a.Analyze(context.Background(), info)
	if err != nil {
		t.Fatal(err)
	}

	if report.MetricsSource != "prometheus" {
		t.Errorf("MetricsSource = %s, expected prometheus", report.MetricsSource)
	}

	// Should have inefficient pods sorted by CPU efficiency
	if len(report.InefficientPods) != 2 {
		t.Fatalf("Expected 2 inefficient pods, got %d", len(report.InefficientPods))
	}

	// nginx should be first (lower efficiency)
	// nginx CPU efficiency: 0.1 / 1.0 = 10%
	nginx := report.InefficientPods[0]
	if nginx.Name != "nginx" {
		t.Errorf("Expected nginx first, got %s", nginx.Name)
	}
	expectedNginxCPUEff := 0.1 // 0.1 cores / 1.0 cores
	if nginx.CPUEfficiency != expectedNginxCPUEff {
		t.Errorf("nginx CPUEfficiency = %v, expected %v", nginx.CPUEfficiency, expectedNginxCPUEff)
	}

	// redis CPU efficiency: 0.4 / 0.5 = 80%
	redis := report.InefficientPods[1]
	if redis.Name != "redis" {
		t.Errorf("Expected redis second, got %s", redis.Name)
	}
	expectedRedisCPUEff := 0.8 // 0.4 cores / 0.5 cores
	if redis.CPUEfficiency != expectedRedisCPUEff {
		t.Errorf("redis CPUEfficiency = %v, expected %v", redis.CPUEfficiency, expectedRedisCPUEff)
	}
}

func TestAnalyzeWithoutPrometheus(t *testing.T) {
	a := New(&mockPricing{price: 0.10})

	// No Prometheus data - CPUUsage and MemoryUsage are 0
	info := &collector.ClusterInfo{
		TotalNodes: 1,
		TotalPods:  1,
		Nodes: []collector.NodeInfo{{
			Name:           "node-1",
			InstanceType:   "t3.medium",
			Region:         "us-east-1",
			CPUAllocatable: 2000,
			MemAllocatable: 4 * 1024 * 1024 * 1024,
			CPUUsage:       0, // No Prometheus
			MemoryUsage:    0, // No Prometheus
			Pods: []collector.PodInfo{
				{CPURequest: 1000, MemoryRequest: 2 * 1024 * 1024 * 1024},
			},
		}},
	}

	report, err := a.Analyze(context.Background(), info)
	if err != nil {
		t.Fatal(err)
	}

	// Without Prometheus, no pod efficiency data
	if len(report.InefficientPods) != 0 {
		t.Errorf("Expected no inefficient pods without Prometheus, got %d", len(report.InefficientPods))
	}

	// Requests should still be populated
	if report.Nodes[0].CPURequested != 0.5 { // 1000/2000
		t.Errorf("CPURequested = %v, expected 0.5", report.Nodes[0].CPURequested)
	}

	// Pod requests 50% of each resource → 50% idle
	if !floatEquals(report.Nodes[0].IdlePercent, 0.5) {
		t.Errorf("IdlePercent = %v, expected ~0.5", report.Nodes[0].IdlePercent)
	}
}

func TestAggregateByNamespace(t *testing.T) {
	pods := []PodEfficiency{
		{Name: "pod-1", Namespace: "argocd", CPURequest: 500, CPUUsage: 0.1, MemRequest: 512 * 1024 * 1024, MemUsage: 100 * 1024 * 1024, MonthlyCost: 10},
		{Name: "pod-2", Namespace: "argocd", CPURequest: 500, CPUUsage: 0.1, MemRequest: 512 * 1024 * 1024, MemUsage: 100 * 1024 * 1024, MonthlyCost: 10},
		{Name: "pod-3", Namespace: "default", CPURequest: 1000, CPUUsage: 0.5, MemRequest: 1024 * 1024 * 1024, MemUsage: 500 * 1024 * 1024, MonthlyCost: 25},
	}

	namespaces := aggregateByNamespace(pods)

	if len(namespaces) != 2 {
		t.Fatalf("expected 2 namespaces, got %d", len(namespaces))
	}

	// Sorted by cost desc: default ($25) first
	if namespaces[0].Name != "default" {
		t.Errorf("expected default first (highest cost), got %s", namespaces[0].Name)
	}
	if namespaces[0].MonthlyCost != 25 {
		t.Errorf("default cost = %v, want 25", namespaces[0].MonthlyCost)
	}

	// argocd: 2 pods, $20 total
	if namespaces[1].PodCount != 2 {
		t.Errorf("argocd pod count = %d, want 2", namespaces[1].PodCount)
	}
	if namespaces[1].MonthlyCost != 20 {
		t.Errorf("argocd cost = %v, want 20", namespaces[1].MonthlyCost)
	}
	if namespaces[1].CPURequest != 1000 {
		t.Errorf("argocd CPURequest = %d, want 1000", namespaces[1].CPURequest)
	}
}

func TestAnalyzePopulatesNamespaces(t *testing.T) {
	a := New(&mockPricing{price: 0.10})

	info := &collector.ClusterInfo{
		TotalNodes: 1,
		TotalPods:  2,
		Nodes: []collector.NodeInfo{{
			Name:           "node-1",
			InstanceType:   "t3.large",
			Region:         "us-east-1",
			CPUAllocatable: 4000,
			MemAllocatable: 8 * 1024 * 1024 * 1024,
			Pods: []collector.PodInfo{
				{Name: "web-1", Namespace: "prod", CPURequest: 500, MemoryRequest: 512 * 1024 * 1024},
				{Name: "web-2", Namespace: "dev", CPURequest: 200, MemoryRequest: 256 * 1024 * 1024},
			},
		}},
	}

	report, err := a.Analyze(context.Background(), info)
	if err != nil {
		t.Fatal(err)
	}

	if len(report.Namespaces) != 2 {
		t.Fatalf("expected 2 namespaces, got %d", len(report.Namespaces))
	}

	if len(report.AllPods) != 2 {
		t.Fatalf("expected 2 AllPods, got %d", len(report.AllPods))
	}
}

func TestMaxRequestUsage(t *testing.T) {
	// When pod usage exceeds request (burst), cost should be based on usage
	a := New(&mockPricing{price: 0.10})

	info := &collector.ClusterInfo{
		TotalNodes: 1,
		TotalPods:  1,
		Nodes: []collector.NodeInfo{{
			Name:           "node-1",
			InstanceType:   "t3.large",
			Region:         "us-east-1",
			CPUAllocatable: 4000,
			MemAllocatable: 8 * 1024 * 1024 * 1024,
			CPUUsage:       3.0, // Prometheus says node uses 3 cores
			MemoryUsage:    6 * 1024 * 1024 * 1024,
			Pods: []collector.PodInfo{
				{
					Name:          "burst-app",
					Namespace:     "default",
					CPURequest:    1000,                    // requests 1 core
					MemoryRequest: 2 * 1024 * 1024 * 1024, // requests 2 GiB
					CPUUsage:      3.0,                     // actually uses 3 cores (burst)
					MemoryUsage:   6 * 1024 * 1024 * 1024,  // actually uses 6 GiB (burst)
				},
			},
		}},
	}

	report, err := a.Analyze(context.Background(), info)
	if err != nil {
		t.Fatal(err)
	}

	pod := report.AllPods[0]

	// max(request, usage) applied: cost based on 3 cores, 6 GiB
	// Without max(req, usage), cost would be based on 1 core, 2 GiB
	// Cost with burst should be ~3x CPU + 3x RAM compared to request-only

	// SplitNodeCost(0.10, 4000, 8GiB)
	cpuPerCore, ramPerGiB := pricing.SplitNodeCost(0.10, 4000, 8*1024*1024*1024)
	expectedCPUCost := 3.0 * cpuPerCore * hoursPerMonth
	expectedRAMCost := 6.0 * ramPerGiB * hoursPerMonth

	if !floatEquals(pod.CPUCost, expectedCPUCost) {
		t.Errorf("CPUCost = %v, expected %v (should use usage, not request)", pod.CPUCost, expectedCPUCost)
	}
	if !floatEquals(pod.RAMCost, expectedRAMCost) {
		t.Errorf("RAMCost = %v, expected %v (should use usage, not request)", pod.RAMCost, expectedRAMCost)
	}
	if !floatEquals(pod.MonthlyCost, pod.CPUCost+pod.RAMCost) {
		t.Errorf("MonthlyCost = %v, expected CPUCost+RAMCost = %v", pod.MonthlyCost, pod.CPUCost+pod.RAMCost)
	}
}

func TestPerResourceIdle(t *testing.T) {
	a := New(&mockPricing{price: 0.10})

	// Node: 4 CPU, 8 GiB
	// Pod: requests 1 CPU (25%), 6 GiB (75%)
	// CPU idle should be high (75%), RAM idle should be low (25%)
	info := &collector.ClusterInfo{
		TotalNodes: 1,
		TotalPods:  1,
		Nodes: []collector.NodeInfo{{
			Name:           "node-1",
			InstanceType:   "m5.xlarge",
			Region:         "us-east-1",
			CPUAllocatable: 4000,
			MemAllocatable: 8 * 1024 * 1024 * 1024,
			Pods: []collector.PodInfo{
				{
					Name:          "mem-heavy",
					Namespace:     "default",
					CPURequest:    1000,                    // 1 core (25% of 4)
					MemoryRequest: 6 * 1024 * 1024 * 1024, // 6 GiB (75% of 8)
				},
			},
		}},
	}

	report, err := a.Analyze(context.Background(), info)
	if err != nil {
		t.Fatal(err)
	}

	node := report.Nodes[0]

	// CPU idle should be larger than RAM idle (more CPU is unallocated)
	if node.CPUIdleCost <= node.RAMIdleCost {
		t.Errorf("CPUIdleCost (%v) should be > RAMIdleCost (%v) for CPU-heavy idle node",
			node.CPUIdleCost, node.RAMIdleCost)
	}

	// Total idle = CPUIdleCost + RAMIdleCost
	if !floatEquals(node.IdleCostMonthly, node.CPUIdleCost+node.RAMIdleCost) {
		t.Errorf("IdleCostMonthly (%v) != CPUIdleCost+RAMIdleCost (%v)",
			node.IdleCostMonthly, node.CPUIdleCost+node.RAMIdleCost)
	}
}

func TestCPUCostPlusRAMCostEqualsMonthlyCost(t *testing.T) {
	a := New(&mockPricing{price: 0.192}) // m5.xlarge price

	info := &collector.ClusterInfo{
		TotalNodes: 1,
		TotalPods:  2,
		Nodes: []collector.NodeInfo{{
			Name:           "node-1",
			InstanceType:   "m5.xlarge",
			Region:         "us-east-1",
			CPUAllocatable: 4000,
			MemAllocatable: 16 * 1024 * 1024 * 1024,
			Pods: []collector.PodInfo{
				{Name: "web", Namespace: "prod", CPURequest: 1000, MemoryRequest: 4 * 1024 * 1024 * 1024},
				{Name: "api", Namespace: "prod", CPURequest: 2000, MemoryRequest: 8 * 1024 * 1024 * 1024},
			},
		}},
	}

	report, err := a.Analyze(context.Background(), info)
	if err != nil {
		t.Fatal(err)
	}

	// For every pod: MonthlyCost = CPUCost + RAMCost
	for _, pod := range report.AllPods {
		if !floatEquals(pod.MonthlyCost, pod.CPUCost+pod.RAMCost) {
			t.Errorf("pod %s: MonthlyCost=%v != CPUCost(%v)+RAMCost(%v)",
				pod.Name, pod.MonthlyCost, pod.CPUCost, pod.RAMCost)
		}
	}

	// For every namespace: MonthlyCost = CPUCost + RAMCost
	for _, ns := range report.Namespaces {
		if !floatEquals(ns.MonthlyCost, ns.CPUCost+ns.RAMCost) {
			t.Errorf("ns %s: MonthlyCost=%v != CPUCost(%v)+RAMCost(%v)",
				ns.Name, ns.MonthlyCost, ns.CPUCost, ns.RAMCost)
		}
	}
}

func TestPVCostCalculation(t *testing.T) {
	pvcs := []collector.PVCInfo{
		{Name: "data-postgres", Namespace: "database", StorageClass: "gp3", RequestedBytes: 100 * 1024 * 1024 * 1024},
		{Name: "redis-data", Namespace: "cache", StorageClass: "gp2", RequestedBytes: 20 * 1024 * 1024 * 1024},
	}

	costs := calculatePVCosts(pvcs, &mockPricing{price: 0.10})

	if len(costs) != 2 {
		t.Fatalf("expected 2 PV costs, got %d", len(costs))
	}

	// gp3: 100GiB × $0.10/GiB/mo = $10/mo (mockPricing returns 0.10 for all)
	if costs[0].CapacityGiB != 100 {
		t.Errorf("capacity = %.0f, expected 100", costs[0].CapacityGiB)
	}
	if !floatEquals(costs[0].MonthlyCost, 10.0) {
		t.Errorf("cost = %.2f, expected 10.00", costs[0].MonthlyCost)
	}
	if costs[0].Namespace != "database" {
		t.Errorf("namespace = %s, expected database", costs[0].Namespace)
	}

	// gp2: 20GiB × $0.10/GiB/mo = $2/mo
	if !floatEquals(costs[1].MonthlyCost, 2.0) {
		t.Errorf("cost = %.2f, expected 2.00", costs[1].MonthlyCost)
	}
}

func TestLBCostCalculation(t *testing.T) {
	lbs := []collector.LBServiceInfo{
		{Name: "app-ingress", Namespace: "ingress"},
		{Name: "api-lb", Namespace: "prod"},
	}

	costs := calculateLBCosts(lbs, &mockPricing{price: 0.10})

	if len(costs) != 2 {
		t.Fatalf("expected 2 LB costs, got %d", len(costs))
	}

	// $0.0225/hr × 730 = $16.43/mo
	expected := 0.0225 * 730
	if !floatEquals(costs[0].MonthlyCost, expected) {
		t.Errorf("LB cost = %.2f, expected %.2f", costs[0].MonthlyCost, expected)
	}
	if costs[0].Namespace != "ingress" {
		t.Errorf("namespace = %s, expected ingress", costs[0].Namespace)
	}
}

func TestStorageCostInNamespace(t *testing.T) {
	namespaces := []NamespaceCost{
		{Name: "database", MonthlyCost: 50},
		{Name: "cache", MonthlyCost: 30},
	}

	pvCosts := []PVCost{
		{Name: "pg-data", Namespace: "database", MonthlyCost: 10},
		{Name: "redis", Namespace: "cache", MonthlyCost: 2},
	}

	addStorageCostToNamespaces(namespaces, pvCosts)

	if namespaces[0].StorageCost != 10 {
		t.Errorf("database storage = %.0f, expected 10", namespaces[0].StorageCost)
	}
	if namespaces[0].MonthlyCost != 60 {
		t.Errorf("database total = %.0f, expected 60 (50+10)", namespaces[0].MonthlyCost)
	}
	if namespaces[1].StorageCost != 2 {
		t.Errorf("cache storage = %.0f, expected 2", namespaces[1].StorageCost)
	}
}

func TestTotalMonthlyCostIncludes(t *testing.T) {
	a := New(&mockPricing{price: 0.10})

	info := &collector.ClusterInfo{
		TotalNodes: 1,
		TotalPods:  1,
		Nodes: []collector.NodeInfo{{
			Name: "node-1", InstanceType: "t3.large", Region: "us-east-1",
			CPUAllocatable: 2000, MemAllocatable: 8 * 1024 * 1024 * 1024,
		}},
		PVCs: []collector.PVCInfo{
			{Name: "data", Namespace: "default", StorageClass: "gp3", RequestedBytes: 50 * 1024 * 1024 * 1024},
		},
		LoadBalancers: []collector.LBServiceInfo{
			{Name: "lb-1", Namespace: "default"},
		},
	}

	report, err := a.Analyze(context.Background(), info)
	if err != nil {
		t.Fatal(err)
	}

	// Compute: $0.10 × 730 = $73
	// PV: 50GiB × $0.10 = $5
	// LB: $0.0225 × 730 = $16.425
	if report.TotalPVCost != 5 {
		t.Errorf("TotalPVCost = %.2f, expected 5.00", report.TotalPVCost)
	}
	if !floatEquals(report.TotalLBCost, 16.425) {
		t.Errorf("TotalLBCost = %.2f, expected 16.425", report.TotalLBCost)
	}
	expectedTotal := report.MonthlyCost + report.TotalPVCost + report.TotalLBCost
	if !floatEquals(report.TotalMonthlyCost, expectedTotal) {
		t.Errorf("TotalMonthlyCost = %.2f, expected %.2f", report.TotalMonthlyCost, expectedTotal)
	}
}
