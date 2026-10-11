package analyzer

import (
	"context"
	"testing"

	"github.com/tanrikuluozlem/burn/internal/collector"
)

func wasteNode(name string, reqCPUMilli int64, reqMemGiB float64, spot bool) collector.NodeInfo {
	return collector.NodeInfo{
		Name:           name,
		InstanceType:   "t3.medium",
		Region:         "eu-central-1",
		IsSpot:         spot,
		CloudProvider:  collector.CloudAWS,
		CPUAllocatable: 2000,
		MemAllocatable: 4 * 1024 * 1024 * 1024,
		Pods: []collector.PodInfo{{
			CPURequest:    reqCPUMilli,
			MemoryRequest: int64(reqMemGiB * 1024 * 1024 * 1024),
		}},
	}
}

func analyzeNodes(t *testing.T, nodes ...collector.NodeInfo) *CostReport {
	t.Helper()
	report, err := New(&mockPricing{price: 0.10}).Analyze(context.Background(), &collector.ClusterInfo{
		TotalNodes: len(nodes),
		TotalPods:  len(nodes),
		Nodes:      nodes,
	})
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	return report
}

func TestPotentialSavings_SingleNodeIsNotSavings(t *testing.T) {
	report := analyzeNodes(t, wasteNode("node-1", 1000, 2, false))

	if !floatEquals(report.Nodes[0].IdlePercent, 0.5) {
		t.Fatalf("fixture IdlePercent = %v, want ~0.5 (must exceed the 0.4 threshold)", report.Nodes[0].IdlePercent)
	}
	if len(report.WasteAnalysis.UnderutilizedNodes) != 1 {
		t.Errorf("UnderutilizedNodes = %d, want 1", len(report.WasteAnalysis.UnderutilizedNodes))
	}
	if report.WasteAnalysis.PotentialSavings != 0 {
		t.Errorf("PotentialSavings = %v, want 0 (a single node cannot be consolidated)", report.WasteAnalysis.PotentialSavings)
	}
}

func TestPotentialSavings_TwoQualifyingNodesBothCounted(t *testing.T) {
	report := analyzeNodes(t,
		wasteNode("node-1", 1000, 2, false),
		wasteNode("node-2", 1000, 2, false),
	)

	if len(report.WasteAnalysis.UnderutilizedNodes) != 2 {
		t.Errorf("UnderutilizedNodes = %d, want 2", len(report.WasteAnalysis.UnderutilizedNodes))
	}
	if !floatEquals(report.WasteAnalysis.PotentialSavings, 146.0) {
		t.Errorf("PotentialSavings = %v, want 146.0 (73.0 + 73.0)", report.WasteAnalysis.PotentialSavings)
	}
}

func TestPotentialSavings_OnlyQualifyingNodeCounted(t *testing.T) {
	report := analyzeNodes(t,
		wasteNode("node-1", 1000, 2, false),
		wasteNode("node-2", 1400, 2.8, false),
	)

	if len(report.WasteAnalysis.UnderutilizedNodes) != 1 {
		t.Errorf("UnderutilizedNodes = %d, want 1", len(report.WasteAnalysis.UnderutilizedNodes))
	}
	if !floatEquals(report.WasteAnalysis.PotentialSavings, 73.0) {
		t.Errorf("PotentialSavings = %v, want 73.0", report.WasteAnalysis.PotentialSavings)
	}
}

func TestPotentialSavings_SingleSpotNodeExcluded(t *testing.T) {
	report := analyzeNodes(t, wasteNode("spot-1", 1000, 2, true))

	if len(report.WasteAnalysis.UnderutilizedNodes) != 1 {
		t.Errorf("UnderutilizedNodes = %d, want 1 (spot nodes stay listed)", len(report.WasteAnalysis.UnderutilizedNodes))
	}
	if report.WasteAnalysis.PotentialSavings != 0 {
		t.Errorf("PotentialSavings = %v, want 0", report.WasteAnalysis.PotentialSavings)
	}
}

func TestPotentialSavings_SingleNodeBelowThreshold(t *testing.T) {
	report := analyzeNodes(t, wasteNode("node-1", 1400, 2.8, false))

	if len(report.WasteAnalysis.UnderutilizedNodes) != 0 {
		t.Errorf("UnderutilizedNodes = %d, want 0", len(report.WasteAnalysis.UnderutilizedNodes))
	}
	if report.WasteAnalysis.PotentialSavings != 0 {
		t.Errorf("PotentialSavings = %v, want 0", report.WasteAnalysis.PotentialSavings)
	}
}

func TestPotentialSavings_ShowcaseClusterShapeUnchanged(t *testing.T) {
	report := analyzeNodes(t,
		wasteNode("spot-high-idle", 1000, 2, true),
		wasteNode("od-1", 1400, 2.8, false),
		wasteNode("od-2", 1400, 2.8, false),
		wasteNode("od-3", 1400, 2.8, false),
		wasteNode("od-4", 1400, 2.8, false),
	)

	if len(report.Nodes) != 5 {
		t.Fatalf("Nodes = %d, want 5", len(report.Nodes))
	}
	if len(report.WasteAnalysis.UnderutilizedNodes) != 1 {
		t.Errorf("UnderutilizedNodes = %d, want 1 (only the spot node is above threshold)", len(report.WasteAnalysis.UnderutilizedNodes))
	}
	if report.WasteAnalysis.PotentialSavings != 0 {
		t.Errorf("PotentialSavings = %v, want 0 (no non-spot node crosses the threshold)", report.WasteAnalysis.PotentialSavings)
	}
}
