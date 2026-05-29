package billing

import (
	"fmt"
	"math"
	"testing"

	"github.com/tanrikuluozlem/burn/internal/collector"
)

func TestParseProviderID(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"aws:///us-east-1a/i-0abc123def", "i-0abc123def"},
		{"aws:///eu-central-1a/i-0xyz789", "i-0xyz789"},
		{"azure:///subscriptions/sub-1/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/aks-node-0", "aks-node-0"},
		{"gce://project/zone/instance-name", "instance-name"},
		{"", ""},
		{"i-standalone", "i-standalone"},
		{"aws:///us-east-1a/", ""},
		{"///", ""},
	}

	for _, tt := range tests {
		got := ParseProviderID(tt.input)
		if got != tt.want {
			t.Errorf("ParseProviderID(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestBuildNodeInstanceMap(t *testing.T) {
	nodes := []collector.NodeInfo{
		{Name: "node-1", ProviderID: "aws:///us-east-1a/i-aaa"},
		{Name: "node-2", ProviderID: "aws:///us-east-1b/i-bbb"},
		{Name: "node-3", ProviderID: ""},
	}

	m := BuildNodeInstanceMap(nodes)

	if m["i-aaa"] != 0 {
		t.Errorf("expected i-aaa → 0, got %d", m["i-aaa"])
	}
	if m["i-bbb"] != 1 {
		t.Errorf("expected i-bbb → 1, got %d", m["i-bbb"])
	}
	if _, ok := m[""]; ok {
		t.Error("empty providerID should not be in map")
	}
	if len(m) != 2 {
		t.Errorf("expected 2 entries, got %d", len(m))
	}
}

func TestAggregateCURByResource(t *testing.T) {
	items := []CURLineItem{
		{ResourceID: "i-aaa", EffectiveCost: 10, UsageAmount: 24, UsageType: "BoxUsage:t3.large"},
		{ResourceID: "i-aaa", EffectiveCost: 10, UsageAmount: 24, UsageType: "BoxUsage:t3.large"},
		{ResourceID: "i-bbb", EffectiveCost: 5, UsageAmount: 24, ReservationARN: "arn:aws:ec2:ri/123"},
		{ResourceID: "", EffectiveCost: 1, UsageAmount: 1},
	}

	agg := AggregateCURByResource(items)

	if len(agg) != 2 {
		t.Fatalf("expected 2 resources, got %d", len(agg))
	}
	if agg["i-aaa"].TotalCost != 20 {
		t.Errorf("i-aaa total = %f, want 20", agg["i-aaa"].TotalCost)
	}
	if agg["i-aaa"].PricingTerm != "OnDemand" {
		t.Errorf("i-aaa term = %s, want OnDemand", agg["i-aaa"].PricingTerm)
	}
	if agg["i-bbb"].PricingTerm != "Reserved" {
		t.Errorf("i-bbb term = %s, want Reserved", agg["i-bbb"].PricingTerm)
	}
}

func TestAggregateCURSpotDetection(t *testing.T) {
	items := []CURLineItem{
		{ResourceID: "i-spot", EffectiveCost: 3, UsageAmount: 24, UsageType: "USE2-SpotUsage:t3.large"},
	}

	agg := AggregateCURByResource(items)

	if agg["i-spot"].PricingTerm != "Spot" {
		t.Errorf("expected Spot, got %s", agg["i-spot"].PricingTerm)
	}
	if agg["i-spot"].SpotCost != 3 {
		t.Errorf("spot cost = %f, want 3", agg["i-spot"].SpotCost)
	}
}

func TestAggregateCURSavingsPlanFallback(t *testing.T) {
	items := []CURLineItem{
		{ResourceID: "i-sp", EffectiveCost: 5, UsageAmount: 24, PricingTerm: "SavingsPlan", UsageType: "BoxUsage:m5.xlarge"},
	}

	agg := AggregateCURByResource(items)

	if agg["i-sp"].PricingTerm != "SavingsPlan" {
		t.Errorf("expected SavingsPlan, got %s", agg["i-sp"].PricingTerm)
	}
}

func TestAggregateCURReservedFallback(t *testing.T) {
	items := []CURLineItem{
		{ResourceID: "i-ri", EffectiveCost: 4, UsageAmount: 24, PricingTerm: "Reserved", UsageType: "BoxUsage:m5.xlarge"},
	}

	agg := AggregateCURByResource(items)

	if agg["i-ri"].PricingTerm != "Reserved" {
		t.Errorf("expected Reserved, got %s", agg["i-ri"].PricingTerm)
	}
}

func TestAggregateCURDataTransferSplit(t *testing.T) {
	items := []CURLineItem{
		{ResourceID: "i-aaa", EffectiveCost: 10, UsageAmount: 24, UsageType: "EUC1-BoxUsage:t3.large"},
		{ResourceID: "i-aaa", EffectiveCost: 2, UsageAmount: 50, UsageType: "EUC1-DataTransfer-Regional-Bytes"},
	}

	agg := AggregateCURByResource(items)

	if agg["i-aaa"].ComputeCost != 10 {
		t.Errorf("compute = %f, want 10", agg["i-aaa"].ComputeCost)
	}
	if agg["i-aaa"].DataTransferCost != 2 {
		t.Errorf("transfer = %f, want 2", agg["i-aaa"].DataTransferCost)
	}
	if agg["i-aaa"].TotalCost != 12 {
		t.Errorf("total = %f, want 12", agg["i-aaa"].TotalCost)
	}
	if agg["i-aaa"].UsageHours != 24 {
		t.Errorf("hours = %f, want 24 (only compute hours)", agg["i-aaa"].UsageHours)
	}
}

func TestMatchNodesToCUR(t *testing.T) {
	nodes := []collector.NodeInfo{
		{Name: "node-1", ProviderID: "aws:///us-east-1a/i-aaa", InstanceType: "t3.large", Region: "us-east-1"},
		{Name: "node-2", ProviderID: "aws:///us-east-1b/i-bbb", InstanceType: "t3.large", Region: "us-east-1"},
		{Name: "node-3", ProviderID: "aws:///us-east-1c/i-ccc", InstanceType: "t3.large", Region: "us-east-1"},
	}

	estimated := map[string]float64{
		"node-1": 70.08,
		"node-2": 70.08,
		"node-3": 70.08,
	}

	curCosts := map[string]*AggregatedCost{
		"i-aaa": {ResourceID: "i-aaa", TotalCost: 50, ComputeCost: 48, DataTransferCost: 2, UsageHours: 168, PricingTerm: "Reserved", RICost: 50},
		"i-bbb": {ResourceID: "i-bbb", TotalCost: 70, ComputeCost: 70, UsageHours: 168, PricingTerm: "OnDemand", OnDemandCost: 70},
	}

	results, unmatchedCUR, unmatchedNodes := MatchNodesToCUR(nodes, estimated, curCosts, 7)

	if len(results) != 2 {
		t.Fatalf("expected 2 matched results, got %d", len(results))
	}
	if unmatchedCUR != 0 {
		t.Errorf("expected 0 unmatched CUR, got %d", unmatchedCUR)
	}
	if unmatchedNodes != 1 {
		t.Errorf("expected 1 unmatched node, got %d", unmatchedNodes)
	}

	for _, r := range results {
		if r.MatchMethod != "provider-id" {
			t.Errorf("%s should match by provider-id, got %s", r.NodeName, r.MatchMethod)
		}
		if r.NodeName == "node-1" && r.ActualTransferCost == 0 {
			t.Error("node-1 should have transfer cost")
		}
	}
}

func TestMatchNodesToCURZeroPeriod(t *testing.T) {
	nodes := []collector.NodeInfo{
		{Name: "node-1", ProviderID: "aws:///us-east-1a/i-aaa"},
	}
	curCosts := map[string]*AggregatedCost{
		"i-aaa": {ResourceID: "i-aaa", TotalCost: 10, ComputeCost: 10, UsageHours: 24},
	}

	results, _, _ := MatchNodesToCUR(nodes, map[string]float64{"node-1": 70}, curCosts, 0)

	for _, r := range results {
		if math.IsInf(r.ActualCost, 0) || math.IsNaN(r.ActualCost) {
			t.Error("zero period should not produce Inf/NaN")
		}
	}
}

func TestMatchNodesToCUREmptyCUR(t *testing.T) {
	nodes := []collector.NodeInfo{
		{Name: "node-1", ProviderID: "aws:///us-east-1a/i-aaa"},
	}

	results, unmatchedCUR, unmatchedNodes := MatchNodesToCUR(nodes, map[string]float64{"node-1": 70}, map[string]*AggregatedCost{}, 7)

	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
	if unmatchedCUR != 0 {
		t.Errorf("expected 0 unmatched CUR, got %d", unmatchedCUR)
	}
	if unmatchedNodes != 1 {
		t.Errorf("expected 1 unmatched node, got %d", unmatchedNodes)
	}
}

func TestMatchNodesToCURLargeCluster(t *testing.T) {
	nodes := make([]collector.NodeInfo, 500)
	estimated := make(map[string]float64)
	curCosts := make(map[string]*AggregatedCost)

	for i := 0; i < 500; i++ {
		id := fmt.Sprintf("i-%05d", i)
		name := fmt.Sprintf("node-%d", i)
		nodes[i] = collector.NodeInfo{
			Name:         name,
			ProviderID:   fmt.Sprintf("aws:///us-east-1a/%s", id),
			InstanceType: "m5.xlarge",
			Region:       "us-east-1",
		}
		estimated[name] = 140.16
		curCosts[id] = &AggregatedCost{
			ResourceID:  id,
			TotalCost:   100,
			ComputeCost: 95,
			DataTransferCost: 5,
			UsageHours:  168,
			PricingTerm: "Reserved",
			RICost:      100,
		}
	}

	results, unmatchedCUR, unmatchedNodes := MatchNodesToCUR(nodes, estimated, curCosts, 7)

	if len(results) != 500 {
		t.Fatalf("expected 500 results, got %d", len(results))
	}
	if unmatchedCUR != 0 {
		t.Errorf("expected 0 unmatched CUR, got %d", unmatchedCUR)
	}
	if unmatchedNodes != 0 {
		t.Errorf("expected 0 unmatched nodes, got %d", unmatchedNodes)
	}

	for _, r := range results {
		if r.PricingTerm != "Reserved" {
			t.Errorf("%s should be Reserved, got %s", r.NodeName, r.PricingTerm)
		}
		if r.ActualTransferCost == 0 {
			t.Errorf("%s should have transfer cost", r.NodeName)
		}
	}
}

func TestMatchNodesToCURDriftAlert(t *testing.T) {
	nodes := []collector.NodeInfo{
		{Name: "node-1", ProviderID: "aws:///us-east-1a/i-aaa"},
	}
	curCosts := map[string]*AggregatedCost{
		"i-aaa": {ResourceID: "i-aaa", TotalCost: 200, ComputeCost: 200, UsageHours: 168, PricingTerm: "OnDemand", OnDemandCost: 200},
	}

	results, _, _ := MatchNodesToCUR(nodes, map[string]float64{"node-1": 70}, curCosts, 7)

	found := false
	for _, r := range results {
		if r.DriftAlert != "" {
			found = true
		}
	}
	if !found {
		t.Error("expected drift alert for node with >20% difference")
	}
}

func TestMatchNodesToCURNoDriftAlert(t *testing.T) {
	nodes := []collector.NodeInfo{
		{Name: "node-1", ProviderID: "aws:///us-east-1a/i-aaa"},
	}
	curCosts := map[string]*AggregatedCost{
		"i-aaa": {ResourceID: "i-aaa", TotalCost: 16, ComputeCost: 16, UsageHours: 168, PricingTerm: "OnDemand", OnDemandCost: 16},
	}

	results, _, _ := MatchNodesToCUR(nodes, map[string]float64{"node-1": 70}, curCosts, 7)

	for _, r := range results {
		if r.DriftAlert != "" {
			t.Errorf("no alert expected for small difference, got: %s", r.DriftAlert)
		}
	}
}

func TestValidateAthenaConfig(t *testing.T) {
	tests := []struct {
		cfg     AthenaConfig
		wantErr bool
		name    string
	}{
		{AthenaConfig{}, true, "empty"},
		{AthenaConfig{Database: "db"}, true, "missing table"},
		{AthenaConfig{Database: "db", Table: "tbl"}, true, "missing output"},
		{AthenaConfig{Database: "db", Table: "tbl", OutputLocation: "s3://bucket/"}, false, "valid"},
		{AthenaConfig{Database: "db; DROP TABLE", Table: "tbl", OutputLocation: "s3://b/"}, true, "SQL injection db"},
		{AthenaConfig{Database: "db", Table: "tbl; DROP", OutputLocation: "s3://b/"}, true, "SQL injection table"},
		{AthenaConfig{Database: "db", Table: "tbl", OutputLocation: "http://evil.com/"}, true, "non-s3 output"},
		{AthenaConfig{Database: "db", Table: "tbl", OutputLocation: "s3://b/", WorkGroup: "custom"}, false, "custom workgroup"},
	}

	for _, tt := range tests {
		err := ValidateAthenaConfig(tt.cfg)
		if (err != nil) != tt.wantErr {
			t.Errorf("%s: err=%v, wantErr=%v", tt.name, err, tt.wantErr)
		}
	}
}
