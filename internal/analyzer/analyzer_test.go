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
			CPUAllocatable: 2,
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
}
