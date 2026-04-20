package analyzer

import (
	"context"
	"testing"

	"github.com/tanrikuluozlem/burn/internal/collector"
)

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

	nc = NodeCost{PodCount: 1, Utilization: 0.05}
	if recommendationFor(nc) != "Very low utilization - consider smaller instance type" {
		t.Error("wrong recommendation for very low util")
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
			CPUAllocatable: 4000,                    // 4 cores in millicores
			MemAllocatable: 8 * 1024 * 1024 * 1024,  // 8GB
			CPUUsage:       2.0,                     // 2 cores from Prometheus
			MemoryUsage:    4 * 1024 * 1024 * 1024,  // 4GB from Prometheus
		}},
	}

	report, err := a.Analyze(context.Background(), info)
	if err != nil {
		t.Fatal(err)
	}
	if report.MetricsSource != "prometheus" {
		t.Errorf("MetricsSource = %s, expected prometheus", report.MetricsSource)
	}
	// CPU: 2.0 / 4.0 = 0.5 (50%)
	// Mem: 4GB / 8GB = 0.5 (50%)
	// Utilization: (0.5 + 0.5) / 2 = 0.5
	if report.Nodes[0].Utilization != 0.5 {
		t.Errorf("Utilization = %v, expected 0.5", report.Nodes[0].Utilization)
	}
}
