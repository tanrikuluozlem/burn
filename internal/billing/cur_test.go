package billing

import (
	"fmt"
	"math"
	"testing"
	"time"

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
		{"azure:///subscriptions/sub-1/resourceGroups/mc_rg/providers/Microsoft.Compute/virtualMachineScaleSets/aks-nodepool1-123-vmss/virtualMachines/0", "aks-nodepool1-123-vmss/0"},
		{"azure:///subscriptions/sub-1/resourceGroups/mc_rg/providers/Microsoft.Compute/virtualMachineScaleSets/aks-pool2-456-vmss/virtualMachines/3", "aks-pool2-456-vmss/3"},
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
	// AWS: spot detected via UsageType containing "SpotUsage"
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

func TestAggregateCURSpotDetectionAzure(t *testing.T) {
	// Azure: spot detected via PricingTerm (not UsageType)
	// Azure uses "BoxUsage" for all VM compute, spot is indicated by PricingModel dimension
	items := []CURLineItem{
		{ResourceID: "aks-spotpool-vmss", EffectiveCost: 0.20, UsageType: "BoxUsage", PricingTerm: "Spot"},
		{ResourceID: "aks-spotpool-vmss", EffectiveCost: 0.001, UsageType: "DataTransfer", PricingTerm: "OnDemand"},
	}

	agg := AggregateCURByResource(items)

	if agg["aks-spotpool-vmss"].PricingTerm != "Spot" {
		t.Errorf("expected Spot, got %s", agg["aks-spotpool-vmss"].PricingTerm)
	}
	if agg["aks-spotpool-vmss"].SpotCost != 0.20 {
		t.Errorf("spot cost = %f, want 0.20", agg["aks-spotpool-vmss"].SpotCost)
	}
	if agg["aks-spotpool-vmss"].ComputeCost != 0.20 {
		t.Errorf("compute cost = %f, want 0.20", agg["aks-spotpool-vmss"].ComputeCost)
	}
	if agg["aks-spotpool-vmss"].DataTransferCost != 0.001 {
		t.Errorf("transfer cost = %f, want 0.001", agg["aks-spotpool-vmss"].DataTransferCost)
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

	results, unmatchedCUR, unmatchedNodes := MatchNodesToCUR(nodes, estimated, curCosts, 7, time.Time{})

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

	results, _, _ := MatchNodesToCUR(nodes, map[string]float64{"node-1": 70}, curCosts, 0, time.Time{})

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

	results, unmatchedCUR, unmatchedNodes := MatchNodesToCUR(nodes, map[string]float64{"node-1": 70}, map[string]*AggregatedCost{}, 7, time.Time{})

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

	results, unmatchedCUR, unmatchedNodes := MatchNodesToCUR(nodes, estimated, curCosts, 7, time.Time{})

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

func TestMatchNodesToCURVMSSSplit(t *testing.T) {
	// Azure Cost Management reports billing at VMSS level, not per-VM.
	// Two nodes in the same scale set should split the cost evenly.
	nodes := []collector.NodeInfo{
		{Name: "aks-pool-vmss000000", ProviderID: "azure:///subscriptions/s/resourceGroups/rg/providers/Microsoft.Compute/virtualMachineScaleSets/aks-pool-vmss/virtualMachines/0"},
		{Name: "aks-pool-vmss000001", ProviderID: "azure:///subscriptions/s/resourceGroups/rg/providers/Microsoft.Compute/virtualMachineScaleSets/aks-pool-vmss/virtualMachines/1"},
	}

	estimated := map[string]float64{
		"aks-pool-vmss000000": 87.60,
		"aks-pool-vmss000001": 87.60,
	}

	// Billing comes as a single VMSS entry (no /virtualMachines suffix)
	curCosts := map[string]*AggregatedCost{
		"aks-pool-vmss": {
			ResourceID:   "aks-pool-vmss",
			TotalCost:    200,
			ComputeCost:  196,
			DataTransferCost: 4,
			UsageHours:   336, // 2 nodes × 168h
			PricingTerm:  "OnDemand",
			OnDemandCost: 200,
		},
	}

	results, unmatchedCUR, unmatchedNodes := MatchNodesToCUR(nodes, estimated, curCosts, 7, time.Time{})

	if len(results) != 2 {
		t.Fatalf("expected 2 results (one per VMSS node), got %d", len(results))
	}
	if unmatchedCUR != 0 {
		t.Errorf("expected 0 unmatched CUR, got %d", unmatchedCUR)
	}
	if unmatchedNodes != 0 {
		t.Errorf("expected 0 unmatched nodes, got %d", unmatchedNodes)
	}

	for _, r := range results {
		if r.MatchMethod != "vmss-split" {
			t.Errorf("%s: match method = %s, want vmss-split", r.NodeName, r.MatchMethod)
		}
		// Each node gets half: $200/2 = $100 raw, projected to monthly
		expectedMonthly := (100.0 / 7) * DaysPerMonth
		if diff := r.ActualCost - expectedMonthly; diff > 0.01 || diff < -0.01 {
			t.Errorf("%s: actual cost = %.2f, want %.2f", r.NodeName, r.ActualCost, expectedMonthly)
		}
		if r.ActualTransferCost == 0 {
			t.Errorf("%s: should have transfer cost from split", r.NodeName)
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

	results, _, _ := MatchNodesToCUR(nodes, map[string]float64{"node-1": 70}, curCosts, 7, time.Time{})

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

	results, _, _ := MatchNodesToCUR(nodes, map[string]float64{"node-1": 70}, curCosts, 7, time.Time{})

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

func TestMatchDisksToPVCs(t *testing.T) {
	pvcs := []collector.PVCInfo{
		{Name: "postgres-data", Namespace: "data", StorageClass: "gp3", RequestedBytes: 64 * 1024 * 1024 * 1024, CloudDiskID: "vol-abc123"},
		{Name: "redis-data", Namespace: "data", StorageClass: "gp3", RequestedBytes: 10 * 1024 * 1024 * 1024, CloudDiskID: "vol-def456"},
	}
	pvEstimates := map[string]float64{"data/postgres-data": 8.00, "data/redis-data": 1.25}
	billingDisks := map[string]*AggregatedCost{
		"vol-abc123":  {ResourceID: "vol-abc123", TotalCost: 1.84},
		"vol-def456":  {ResourceID: "vol-def456", TotalCost: 0.29},
		"vol-orphan1": {ResourceID: "vol-orphan1", TotalCost: 0.50},
	}

	matched, orphaned := MatchDisksToPVCs(pvcs, pvEstimates, billingDisks, nil, 7)

	if len(matched) != 2 {
		t.Fatalf("expected 2 matched disks, got %d", len(matched))
	}
	if len(orphaned) != 1 {
		t.Fatalf("expected 1 orphaned disk, got %d", len(orphaned))
	}
	if orphaned[0].DiskName != "vol-orphan1" {
		t.Errorf("orphaned disk = %s, want vol-orphan1", orphaned[0].DiskName)
	}
	if !orphaned[0].IsOrphaned {
		t.Error("orphaned disk should have IsOrphaned=true")
	}
}

func TestMatchDisksToPVCsOSDisk(t *testing.T) {
	billingDisks := map[string]*AggregatedCost{
		"aks-nodepool1-disk1_abc": {ResourceID: "aks-nodepool1-disk1_abc", TotalCost: 5.04},
	}
	nodeNames := []string{"aks-nodepool1-vmss000000"}

	matched, orphaned := MatchDisksToPVCs(nil, nil, billingDisks, nodeNames, 7)

	if len(orphaned) != 0 {
		t.Errorf("OS disk should not be orphaned, got %d orphaned", len(orphaned))
	}
	if len(matched) != 1 {
		t.Fatalf("expected 1 matched (OS disk), got %d", len(matched))
	}
	if matched[0].MatchMethod != "os-disk" {
		t.Errorf("match method = %s, want os-disk", matched[0].MatchMethod)
	}
}

func TestMatchLBsToServices(t *testing.T) {
	services := []collector.LBServiceInfo{
		{Name: "nginx-lb", Namespace: "app-test", Hostname: "my-elb.us-east-1.elb.amazonaws.com"},
	}
	lbEstimates := map[string]float64{"app-test/nginx-lb": 16.43}
	billingLBs := map[string]*AggregatedCost{
		"my-elb.us-east-1.elb.amazonaws.com": {ResourceID: "my-elb.us-east-1.elb.amazonaws.com", TotalCost: 4.20},
	}

	matched, orphaned := MatchLBsToServices(services, lbEstimates, billingLBs, 7)

	if len(matched) != 1 {
		t.Fatalf("expected 1 matched LB, got %d", len(matched))
	}
	if matched[0].MatchMethod != "hostname" {
		t.Errorf("match method = %s, want hostname", matched[0].MatchMethod)
	}
	if len(orphaned) != 0 {
		t.Errorf("expected 0 orphaned LBs, got %d", len(orphaned))
	}
}

func TestMatchLBsToServicesOrphaned(t *testing.T) {
	billingLBs := map[string]*AggregatedCost{
		"unknown-lb": {ResourceID: "unknown-lb", TotalCost: 1.00},
	}

	_, orphaned := MatchLBsToServices(nil, nil, billingLBs, 7)

	if len(orphaned) != 1 {
		t.Fatalf("expected 1 orphaned LB, got %d", len(orphaned))
	}
	if !orphaned[0].IsOrphaned {
		t.Error("should be orphaned")
	}
}

func TestDetectCoverageGaps(t *testing.T) {
	nodes := []NodeReconciliation{
		{NodeName: "node-1", InstanceType: "m5.xlarge", PricingTerm: "OnDemand", ActualCost: 140.16},
		{NodeName: "node-2", InstanceType: "m5.xlarge", PricingTerm: "Reserved", ActualCost: 90.00},
		{NodeName: "node-3", InstanceType: "t3.small", PricingTerm: "OnDemand", ActualCost: 15.00},
	}

	gaps := DetectCoverageGaps(nodes)

	if len(gaps) != 1 {
		t.Fatalf("expected 1 coverage gap (node-1 on-demand >$50), got %d", len(gaps))
	}
	if gaps[0].NodeName != "node-1" {
		t.Errorf("gap node = %s, want node-1", gaps[0].NodeName)
	}
	expectedSaving := 140.16 * 0.30
	if math.Abs(gaps[0].PotentialSaving-expectedSaving) > 0.01 {
		t.Errorf("potential saving = %.2f, want %.2f", gaps[0].PotentialSaving, expectedSaving)
	}
}

func TestAggregateCURPartialSPCoverage(t *testing.T) {
	// Azure returns two rows for the same VMSS when SP is partially active:
	// one OnDemand (pre-SP days) and one SavingsPlan (post-SP days).
	// The aggregated PricingTerm should be SavingsPlan since the node has SP coverage.
	items := []CURLineItem{
		{ResourceID: "aks-nodepool1-vmss", EffectiveCost: 11.36, UsageType: "BoxUsage", PricingTerm: "OnDemand"},
		{ResourceID: "aks-nodepool1-vmss", EffectiveCost: 0.12, UsageType: "BoxUsage", PricingTerm: "SavingsPlan"},
	}

	agg := AggregateCURByResource(items)

	if agg["aks-nodepool1-vmss"].PricingTerm != "SavingsPlan" {
		t.Errorf("PricingTerm = %s, want SavingsPlan (node has SP coverage)", agg["aks-nodepool1-vmss"].PricingTerm)
	}
	if agg["aks-nodepool1-vmss"].SPCost != 0.12 {
		t.Errorf("SPCost = %f, want 0.12", agg["aks-nodepool1-vmss"].SPCost)
	}
	if agg["aks-nodepool1-vmss"].OnDemandCost != 11.36 {
		t.Errorf("OnDemandCost = %f, want 11.36", agg["aks-nodepool1-vmss"].OnDemandCost)
	}
	if math.Abs(agg["aks-nodepool1-vmss"].TotalCost-11.48) > 0.001 {
		t.Errorf("TotalCost = %f, want 11.48", agg["aks-nodepool1-vmss"].TotalCost)
	}
}

func TestAggregateCURPartialRICoverage(t *testing.T) {
	// Same pattern for RI: node transitions from OnDemand to Reserved mid-period.
	items := []CURLineItem{
		{ResourceID: "i-aaa", EffectiveCost: 50, UsageType: "BoxUsage:m5.xlarge", PricingTerm: "OnDemand"},
		{ResourceID: "i-aaa", EffectiveCost: 20, UsageType: "BoxUsage:m5.xlarge", PricingTerm: "Reserved", ReservationARN: "arn:aws:ec2:ri/456"},
	}

	agg := AggregateCURByResource(items)

	if agg["i-aaa"].PricingTerm != "Reserved" {
		t.Errorf("PricingTerm = %s, want Reserved", agg["i-aaa"].PricingTerm)
	}
	if agg["i-aaa"].RICost != 20 {
		t.Errorf("RICost = %f, want 20", agg["i-aaa"].RICost)
	}
	if agg["i-aaa"].OnDemandCost != 50 {
		t.Errorf("OnDemandCost = %f, want 50", agg["i-aaa"].OnDemandCost)
	}
}

func TestDetectCoverageGapsSkipsPartialSP(t *testing.T) {
	nodes := []NodeReconciliation{
		{NodeName: "partial-sp", InstanceType: "m5.xlarge", PricingTerm: "SavingsPlan", ActualCost: 140, OnDemandCost: 133, SPCost: 7},
		{NodeName: "full-sp", InstanceType: "m5.xlarge", PricingTerm: "SavingsPlan", ActualCost: 90, SPCost: 90},
		{NodeName: "no-coverage", InstanceType: "m5.xlarge", PricingTerm: "OnDemand", ActualCost: 140, OnDemandCost: 140},
		{NodeName: "spot-node", InstanceType: "m5.xlarge", PricingTerm: "Spot", ActualCost: 60, SpotCost: 60},
		{NodeName: "cheap-od", InstanceType: "t3.small", PricingTerm: "OnDemand", ActualCost: 15, OnDemandCost: 15},
	}

	gaps := DetectCoverageGaps(nodes)

	if len(gaps) != 1 {
		t.Fatalf("expected 1 coverage gap (no-coverage), got %d", len(gaps))
	}
	if gaps[0].NodeName != "no-coverage" {
		t.Errorf("gap node = %s, want no-coverage", gaps[0].NodeName)
	}
}

func TestClassifyAzureResource(t *testing.T) {
	tests := []struct {
		resourceType  string
		meterCategory string
		want          string
	}{
		{"Microsoft.Compute/virtualMachines", "Virtual Machines", CategoryCompute},
		{"microsoft.compute/virtualmachinescalesets", "Virtual Machines", CategoryCompute},
		{"microsoft.compute/virtualmachinescalesets", "Bandwidth", CategoryCompute},
		{"microsoft.compute/virtualmachinescalesets", "Storage", ""},
		{"microsoft.compute/disks", "Storage", CategoryDisk},
		{"microsoft.network/loadbalancers", "Load Balancer", CategoryNetwork},
		{"microsoft.network/publicipaddresses", "Virtual Network", CategoryNetwork},
		{"Microsoft.Storage/storageAccounts", "Storage", ""},
		{"Microsoft.Network/virtualNetworks", "Networking", ""},
	}

	for _, tt := range tests {
		got := classifyAzureResource(tt.resourceType, tt.meterCategory)
		if got != tt.want {
			t.Errorf("classifyAzureResource(%q, %q) = %q, want %q",
				tt.resourceType, tt.meterCategory, got, tt.want)
		}
	}
}
