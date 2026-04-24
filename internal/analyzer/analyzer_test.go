package analyzer

import (
	"context"
	"math"
	"testing"

	"github.com/tanrikuluozlem/burn/internal/collector"
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
		TotalPods:  2,
		Nodes: []collector.NodeInfo{{
			Name:           "node-1",
			InstanceType:   "t3.medium",
			Region:         "us-east-1",
			CPUAllocatable: 4000,                   // 4 cores in millicores
			MemAllocatable: 8 * 1024 * 1024 * 1024, // 8GB
			CPUUsage:       2.0,                    // 2 cores from Prometheus
			MemoryUsage:    4 * 1024 * 1024 * 1024, // 4GB from Prometheus
		}},
	}

	report, err := a.Analyze(context.Background(), info)
	if err != nil {
		t.Fatal(err)
	}
	if report.MetricsSource != "prometheus" {
		t.Errorf("MetricsSource = %s, expected prometheus", report.MetricsSource)
	}
	// CPU: 2.0 / 4.0 = 0.5 (50% used)
	// Mem: 4GB / 8GB = 0.5 (50% used)
	// Used = max(0.5, 0.5) = 0.5
	// Idle = 1 - 0.5 = 0.5 (50%)
	if report.Nodes[0].IdlePercent != 0.5 {
		t.Errorf("IdlePercent = %v, expected 0.5", report.Nodes[0].IdlePercent)
	}
	// Idle cost = 0.10 * 0.5 = 0.05 hourly
	if report.Nodes[0].IdleCostHourly != 0.05 {
		t.Errorf("IdleCostHourly = %v, expected 0.05", report.Nodes[0].IdleCostHourly)
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

	// Without Prometheus, idle is based on requests
	// Used = max(0.25, 0.25) = 0.25
	// Idle = 1 - 0.25 = 0.75 (75%)
	expectedIdle := 0.75
	if node.IdlePercent != expectedIdle {
		t.Errorf("IdlePercent = %v, expected %v", node.IdlePercent, expectedIdle)
	}

	// Idle cost = 0.10 * 0.75 = 0.075 hourly
	expectedIdleCostHourly := 0.075
	if !floatEquals(node.IdleCostHourly, expectedIdleCostHourly) {
		t.Errorf("IdleCostHourly = %v, expected %v", node.IdleCostHourly, expectedIdleCostHourly)
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

	// Idle based on requests: 1 - max(0.5, 0.5) = 0.5
	expectedIdle := 0.5
	if report.Nodes[0].IdlePercent != expectedIdle {
		t.Errorf("IdlePercent = %v, expected %v", report.Nodes[0].IdlePercent, expectedIdle)
	}
}
