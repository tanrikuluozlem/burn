package billing

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/costmanagement/armcostmanagement"
	"github.com/tanrikuluozlem/burn/internal/analyzer"
	"github.com/tanrikuluozlem/burn/internal/collector"
)

func buildMockUsageResponse(rows [][]any) armcostmanagement.QueryClientUsageResponse {
	if rows == nil {
		return armcostmanagement.QueryClientUsageResponse{}
	}
	columns := []*armcostmanagement.QueryColumn{
		{Name: strPtr("Cost")},
		{Name: strPtr("ResourceId")},
		{Name: strPtr("ResourceType")},
		{Name: strPtr("MeterCategory")},
		{Name: strPtr("PricingModel")},
	}
	return armcostmanagement.QueryClientUsageResponse{
		QueryResult: armcostmanagement.QueryResult{
			Properties: &armcostmanagement.QueryProperties{
				Columns: columns,
				Rows:    rows,
			},
		},
	}
}

func TestParseProviderIDAzure(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"/subscriptions/sub-1/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/aks-node-0", "aks-node-0"},
		{"/subscriptions/sub-1/resourceGroups/mc_rg/providers/Microsoft.Compute/virtualMachineScaleSets/aks-nodepool1-123-vmss/virtualMachines/0", "aks-nodepool1-123-vmss/0"},
		{"/subscriptions/sub-1/resourceGroups/mc_rg/providers/Microsoft.Compute/virtualMachineScaleSets/aks-pool2-456-vmss/virtualMachines/3", "aks-pool2-456-vmss/3"},
		{"simple-name", "simple-name"},
		{"", ""},
		{"/", ""},
	}

	for _, tt := range tests {
		got := ParseProviderID(tt.input)
		if got != tt.want {
			t.Errorf("ParseProviderID(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestParseAzureCostResultEmpty(t *testing.T) {
	result := buildMockUsageResponse(nil)
	items := parseAzureCostResult(result)
	if len(items) != 0 {
		t.Errorf("expected 0 items for nil properties, got %d", len(items))
	}
}

func TestParseAzureCostResultClassifiesResources(t *testing.T) {
	rows := [][]any{
		{float64(100.0), "/subscriptions/s/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/node-1", "Microsoft.Compute/virtualMachines", "Virtual Machines", "OnDemand"},
		{float64(50.0), "/subscriptions/s/resourceGroups/rg/providers/Microsoft.Storage/storageAccounts/sa1", "Microsoft.Storage/storageAccounts", "Storage", "OnDemand"},
		{float64(5.0), "/subscriptions/s/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/node-1", "Microsoft.Compute/virtualMachines", "Networking", "OnDemand"},
		{float64(0.003), "/subscriptions/s/resourceGroups/rg/providers/microsoft.compute/virtualmachinescalesets/aks-pool-vmss", "microsoft.compute/virtualmachinescalesets", "Bandwidth", "OnDemand"},
		{float64(0.72), "/subscriptions/s/resourceGroups/rg/providers/microsoft.compute/disks/osdisk-1", "microsoft.compute/disks", "Storage", "OnDemand"},
		{float64(0.0), "/subscriptions/s/resourceGroups/rg/providers/microsoft.network/loadbalancers/kubernetes", "microsoft.network/loadbalancers", "Load Balancer", "OnDemand"},
		{float64(0.12), "/subscriptions/s/resourceGroups/rg/providers/microsoft.network/publicipaddresses/pip-1", "microsoft.network/publicipaddresses", "Virtual Network", "OnDemand"},
	}

	result := buildMockUsageResponse(rows)
	items := parseAzureCostResult(result)

	// storageAccounts filtered out, rest classified
	counts := make(map[string]int)
	for _, item := range items {
		counts[item.Category]++
	}

	if counts[CategoryCompute] != 3 {
		t.Errorf("compute items = %d, want 3 (VM + networking + bandwidth)", counts[CategoryCompute])
	}
	if counts[CategoryDisk] != 1 {
		t.Errorf("disk items = %d, want 1", counts[CategoryDisk])
	}
	if counts[CategoryNetwork] != 2 {
		t.Errorf("network items = %d, want 2 (LB + public IP)", counts[CategoryNetwork])
	}

	// Verify bandwidth is DataTransfer
	for _, item := range items {
		if item.Category == CategoryCompute && item.UsageType == "DataTransfer" {
			return // found bandwidth item correctly classified
		}
	}
	t.Error("bandwidth item not classified as DataTransfer")
}

func TestParseAzureCostResultWithDateColumn(t *testing.T) {
	// Simulates real Azure API response with daily granularity (extra date column)
	columns := []*armcostmanagement.QueryColumn{
		{Name: strPtr("Cost")},
		{Name: strPtr("UsageDate")},
		{Name: strPtr("ResourceId")},
		{Name: strPtr("ResourceType")},
		{Name: strPtr("MeterCategory")},
		{Name: strPtr("Currency")},
	}
	rows := [][]any{
		{float64(5.76), float64(20260530), "/subscriptions/s/resourceGroups/rg/providers/microsoft.compute/virtualmachinescalesets/aks-nodepool1-32627587-vmss", "microsoft.compute/virtualmachinescalesets", "Virtual Machines", "USD"},
		{float64(0.70), float64(20260530), "/subscriptions/s/resourceGroups/rg/providers/microsoft.compute/disks/disk1", "microsoft.compute/disks", "Storage", "USD"},
	}

	result := armcostmanagement.QueryClientUsageResponse{
		QueryResult: armcostmanagement.QueryResult{
			Properties: &armcostmanagement.QueryProperties{
				Columns: columns,
				Rows:    rows,
			},
		},
	}

	items := parseAzureCostResult(result)

	if len(items) != 2 {
		t.Fatalf("expected 2 items (VM + disk), got %d", len(items))
	}

	// Find compute and disk items by category
	var compute, disk *CURLineItem
	for i := range items {
		switch items[i].Category {
		case CategoryCompute:
			compute = &items[i]
		case CategoryDisk:
			disk = &items[i]
		}
	}

	if compute == nil {
		t.Fatal("missing compute item")
	}
	if compute.EffectiveCost != 5.76 {
		t.Errorf("compute cost = %f, want 5.76", compute.EffectiveCost)
	}
	if compute.ResourceID != "aks-nodepool1-32627587-vmss" {
		t.Errorf("compute resource = %s, want aks-nodepool1-32627587-vmss", compute.ResourceID)
	}

	if disk == nil {
		t.Fatal("missing disk item")
	}
	if disk.EffectiveCost != 0.70 {
		t.Errorf("disk cost = %f, want 0.70", disk.EffectiveCost)
	}
	if disk.ResourceID != "disk1" {
		t.Errorf("disk resource = %s, want disk1", disk.ResourceID)
	}
}

func TestParseAzureCostResultPricingModels(t *testing.T) {
	rows := [][]any{
		{float64(80.0), "/subscriptions/s/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/ri-node", "Microsoft.Compute/virtualMachines", "Virtual Machines", "Reservation"},
		{float64(90.0), "/subscriptions/s/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/sp-node", "Microsoft.Compute/virtualMachines", "Virtual Machines", "SavingsPlan"},
		{float64(20.0), "/subscriptions/s/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/spot-node", "Microsoft.Compute/virtualMachines", "Virtual Machines", "Spot"},
		{float64(100.0), "/subscriptions/s/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/od-node", "Microsoft.Compute/virtualMachines", "Virtual Machines", "OnDemand"},
	}

	result := buildMockUsageResponse(rows)
	items := parseAzureCostResult(result)

	if len(items) != 4 {
		t.Fatalf("expected 4 items, got %d", len(items))
	}

	expected := map[string]string{
		"ri-node":   "Reserved",
		"sp-node":   "SavingsPlan",
		"spot-node": "Spot",
		"od-node":   "OnDemand",
	}
	for _, item := range items {
		want, ok := expected[item.ResourceID]
		if !ok {
			t.Errorf("unexpected resource: %s", item.ResourceID)
			continue
		}
		if item.PricingTerm != want {
			t.Errorf("%s: pricing term = %s, want %s", item.ResourceID, item.PricingTerm, want)
		}
	}
}

func TestParseAzureCostResultMissingColumns(t *testing.T) {
	// No column headers
	result := armcostmanagement.QueryClientUsageResponse{
		QueryResult: armcostmanagement.QueryResult{
			Properties: &armcostmanagement.QueryProperties{
				Columns: []*armcostmanagement.QueryColumn{},
				Rows:    [][]any{{float64(10.0), "resource"}},
			},
		},
	}

	items := parseAzureCostResult(result)
	if len(items) != 0 {
		t.Errorf("expected 0 items for missing columns, got %d", len(items))
	}
}

func TestParsePricingModel(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Reservation", "Reserved"},
		{"reservation", "Reserved"},
		{"RESERVATION", "Reserved"},
		{"SavingsPlan", "SavingsPlan"},
		{"savingsplan", "SavingsPlan"},
		{"Spot", "Spot"},
		{"spot", "Spot"},
		{"OnDemand", "OnDemand"},
		{"", "OnDemand"},
		{"UnknownModel", "OnDemand"},
	}

	for _, tt := range tests {
		got := parsePricingModel(tt.input)
		if got != tt.want {
			t.Errorf("parsePricingModel(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestExtractAzureResourceName(t *testing.T) {
	tests := []struct {
		resourceID string
		category   string
		want       string
	}{
		// Compute: uses ParseProviderID (handles VMSS)
		{"/subscriptions/s/resourceGroups/rg/providers/Microsoft.Compute/virtualMachineScaleSets/aks-pool-vmss/virtualMachines/0", CategoryCompute, "aks-pool-vmss/0"},
		{"/subscriptions/s/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/node-1", CategoryCompute, "node-1"},
		// Non-compute: uses last path segment
		{"/subscriptions/s/resourceGroups/rg/providers/microsoft.compute/disks/osdisk-1", CategoryDisk, "osdisk-1"},
		{"/subscriptions/s/resourceGroups/rg/providers/microsoft.network/loadbalancers/kubernetes", CategoryNetwork, "kubernetes"},
		{"/subscriptions/s/resourceGroups/rg/providers/microsoft.network/publicipaddresses/pip-1", CategoryNetwork, "pip-1"},
		// Edge: empty
		{"", CategoryCompute, ""},
	}

	for _, tt := range tests {
		got := extractAzureResourceName(tt.resourceID, tt.category)
		if got != tt.want {
			t.Errorf("extractAzureResourceName(%q, %q) = %q, want %q", tt.resourceID, tt.category, got, tt.want)
		}
	}
}

func TestToFloat64(t *testing.T) {
	tests := []struct {
		input any
		want  float64
		ok    bool
	}{
		{float64(42.5), 42.5, true},
		{float32(3.14), 3.140000104904175, true},
		{int(100), 100, true},
		{"99.9", 99.9, true},
		{"notanumber", 0, false},
		{nil, 0, false},
		{true, 0, false},
	}

	for _, tt := range tests {
		got, ok := toFloat64(tt.input)
		if ok != tt.ok {
			t.Errorf("toFloat64(%v) ok = %v, want %v", tt.input, ok, tt.ok)
		}
		if ok && math.Abs(got-tt.want) > 0.001 {
			t.Errorf("toFloat64(%v) = %f, want %f", tt.input, got, tt.want)
		}
	}
}

func TestParseAzureCostResultVMSSStorageFiltered(t *testing.T) {
	// VMSS resources with "Storage" meter should be filtered (they're OS disks, not data disks).
	// Only VMSS + "Virtual Machines" or "Bandwidth" etc. should pass through.
	rows := [][]any{
		{float64(100.0), "/subscriptions/s/rg/providers/microsoft.compute/virtualmachinescalesets/aks-vmss", "microsoft.compute/virtualmachinescalesets", "Virtual Machines", "OnDemand"},
		{float64(5.0), "/subscriptions/s/rg/providers/microsoft.compute/virtualmachinescalesets/aks-vmss", "microsoft.compute/virtualmachinescalesets", "Storage", "OnDemand"},
	}

	result := buildMockUsageResponse(rows)
	items := parseAzureCostResult(result)

	if len(items) != 1 {
		t.Fatalf("expected 1 item (VMSS storage filtered), got %d", len(items))
	}
	if items[0].EffectiveCost != 100.0 {
		t.Errorf("expected VM cost 100.0, got %f", items[0].EffectiveCost)
	}
}

func TestParseAzureCostResultZeroCostIncluded(t *testing.T) {
	// Zero cost rows should still be included (e.g., reserved instances with amortized = 0 on some days)
	rows := [][]any{
		{float64(0.0), "/subscriptions/s/rg/providers/Microsoft.Compute/virtualMachines/ri-node", "Microsoft.Compute/virtualMachines", "Virtual Machines", "Reservation"},
	}

	result := buildMockUsageResponse(rows)
	items := parseAzureCostResult(result)

	if len(items) != 1 {
		t.Fatalf("expected 1 item (zero cost included), got %d", len(items))
	}
	if items[0].PricingTerm != "Reserved" {
		t.Errorf("expected Reserved, got %s", items[0].PricingTerm)
	}
}

func TestParseAzureCostResultManagementFee(t *testing.T) {
	// AKS management fee is classified via MeterCategory, not ResourceType
	rows := [][]any{
		{float64(73.0), "/subscriptions/s/rg/providers/Microsoft.ContainerService/managedClusters/my-aks", "Microsoft.ContainerService/managedClusters", "Azure Kubernetes Service", "OnDemand"},
	}

	result := buildMockUsageResponse(rows)
	items := parseAzureCostResult(result)

	if len(items) != 1 {
		t.Fatalf("expected 1 management item, got %d", len(items))
	}
	if items[0].Category != CategoryManagement {
		t.Errorf("expected Management category, got %s", items[0].Category)
	}
}

func TestParseAzureCostResultNetworkDetection(t *testing.T) {
	// "Network", "Bandwidth", and "Virtual Network" meter categories should produce DataTransfer usageType
	rows := [][]any{
		{float64(1.0), "/subscriptions/s/rg/providers/Microsoft.Compute/virtualMachines/node-1", "Microsoft.Compute/virtualMachines", "Networking", "OnDemand"},
		{float64(2.0), "/subscriptions/s/rg/providers/Microsoft.Compute/virtualMachines/node-1", "Microsoft.Compute/virtualMachines", "Bandwidth", "OnDemand"},
		{float64(3.0), "/subscriptions/s/rg/providers/microsoft.network/publicipaddresses/pip-1", "microsoft.network/publicipaddresses", "Virtual Network", "OnDemand"},
	}

	result := buildMockUsageResponse(rows)
	items := parseAzureCostResult(result)

	for _, item := range items {
		if item.UsageType != "DataTransfer" {
			t.Errorf("resource %s meter %q: usageType = %s, want DataTransfer", item.ResourceID, item.PricingTerm, item.UsageType)
		}
	}
}

func TestReconcileAzureEndToEnd(t *testing.T) {
	// Simulate a complete Azure reconciliation with mock parsed data.
	// This tests the ReconcileAzure flow without hitting the API.
	nodes := []collector.NodeInfo{
		{
			Name:          "aks-pool-vmss000000",
			ProviderID:    "azure:///subscriptions/s/resourceGroups/rg/providers/Microsoft.Compute/virtualMachineScaleSets/aks-pool-vmss/virtualMachines/0",
			InstanceType:  "Standard_D4s_v3",
			Region:        "eastus",
			CloudProvider: collector.CloudAzure,
		},
		{
			Name:          "aks-pool-vmss000001",
			ProviderID:    "azure:///subscriptions/s/resourceGroups/rg/providers/Microsoft.Compute/virtualMachineScaleSets/aks-pool-vmss/virtualMachines/1",
			InstanceType:  "Standard_D4s_v3",
			Region:        "eastus",
			CloudProvider: collector.CloudAzure,
		},
		{
			Name:          "aks-spot-vmss000000",
			ProviderID:    "azure:///subscriptions/s/resourceGroups/rg/providers/Microsoft.Compute/virtualMachineScaleSets/aks-spot-vmss/virtualMachines/0",
			InstanceType:  "Standard_D4s_v3",
			Region:        "eastus",
			IsSpot:        true,
			CloudProvider: collector.CloudAzure,
		},
	}

	estimatedCosts := map[string]float64{
		"aks-pool-vmss000000": 140.16,
		"aks-pool-vmss000001": 140.16,
		"aks-spot-vmss000000": 42.05,
	}

	namespaces := []analyzer.NamespaceCost{
		{Name: "app", PodCount: 5, MonthlyCost: 200},
		{Name: "system", PodCount: 3, MonthlyCost: 80},
	}

	pvcs := []collector.PVCInfo{
		{Name: "data-pvc", Namespace: "app", StorageClass: "managed-premium", RequestedBytes: 100 * 1024 * 1024 * 1024, CloudDiskID: "data-disk-1"},
	}
	pvEstimates := map[string]float64{"app/data-pvc": 12.50}

	lbs := []collector.LBServiceInfo{
		{Name: "nginx", Namespace: "app", Hostname: "kubernetes"},
	}
	lbEstimates := map[string]float64{"app/nginx": 3.65}

	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	periodDays := 7.0

	// Build fake CUR items as if they came from Azure Cost Management API
	computeItems := []CURLineItem{
		{ResourceID: "aks-pool-vmss", EffectiveCost: 200, UsageType: "BoxUsage", PricingTerm: "OnDemand", Category: CategoryCompute},
		{ResourceID: "aks-spot-vmss", EffectiveCost: 10, UsageType: "BoxUsage", PricingTerm: "Spot", Category: CategoryCompute},
	}

	curCosts := AggregateCURByResource(computeItems)
	matched, unmatchedCUR, unmatchedNodes := MatchNodesToCUR(nodes, estimatedCosts, curCosts, periodDays, start)

	// Verify VMSS split: 2 nodes share $200, each gets $100
	if len(matched) != 3 {
		t.Fatalf("expected 3 matched nodes, got %d", len(matched))
	}

	// All nodes match via vmss-split (single-node VMSS still uses the split path)
	for _, n := range matched {
		if n.MatchMethod != "vmss-split" {
			t.Errorf("%s: match method = %s, want vmss-split", n.NodeName, n.MatchMethod)
		}
	}

	// Verify cost split: pool-vmss ($200) / 2 nodes = $100 each; spot-vmss ($10) / 1 node = $10
	for _, n := range matched {
		var wantRaw float64
		switch {
		case n.NodeName == "aks-pool-vmss000000" || n.NodeName == "aks-pool-vmss000001":
			wantRaw = 100
			if n.PricingTerm != "OnDemand" {
				t.Errorf("%s: term = %s, want OnDemand", n.NodeName, n.PricingTerm)
			}
		case n.NodeName == "aks-spot-vmss000000":
			wantRaw = 10
			if n.PricingTerm != "Spot" {
				t.Errorf("%s: term = %s, want Spot", n.NodeName, n.PricingTerm)
			}
		}
		wantMonthly := wantRaw / 7 * DaysPerMonth
		if math.Abs(n.ActualCost-wantMonthly) > 0.01 {
			t.Errorf("%s: actual = %.2f, want %.2f", n.NodeName, n.ActualCost, wantMonthly)
		}
	}
	if unmatchedCUR != 0 {
		t.Errorf("expected 0 unmatched CUR, got %d", unmatchedCUR)
	}
	if unmatchedNodes != 0 {
		t.Errorf("expected 0 unmatched nodes, got %d", unmatchedNodes)
	}

	// Verify disk matching
	diskItems := []CURLineItem{
		{ResourceID: "data-disk-1", EffectiveCost: 2.10, Category: CategoryDisk},
	}
	diskCosts := AggregateCURByResource(diskItems)
	var nodeNames []string
	for _, n := range nodes {
		nodeNames = append(nodeNames, n.Name)
	}
	matchedDisks, orphanedDisks := MatchDisksToPVCs(pvcs, pvEstimates, diskCosts, nodeNames, periodDays)
	if len(matchedDisks) != 1 || matchedDisks[0].PVCName != "data-pvc" {
		t.Errorf("disk matching failed: matched=%d", len(matchedDisks))
	}
	if len(orphanedDisks) != 0 {
		t.Errorf("expected 0 orphaned disks, got %d", len(orphanedDisks))
	}

	// Verify LB matching by name
	lbItems := []CURLineItem{
		{ResourceID: "kubernetes", EffectiveCost: 0.84, InstanceType: "microsoft.network/loadbalancers", Category: CategoryNetwork},
	}
	lbCosts := AggregateCURByResource(lbItems)
	matchedLBs, orphanedLBs := MatchLBsToServices(lbs, lbEstimates, lbCosts, periodDays)
	if len(matchedLBs) != 1 {
		t.Errorf("expected 1 matched LB, got %d", len(matchedLBs))
	}
	if len(orphanedLBs) != 0 {
		t.Errorf("expected 0 orphaned LBs, got %d", len(orphanedLBs))
	}

	// Verify namespace reconciliation (proportional — no split cost for Azure)
	var totalEst, totalActual float64
	for _, n := range matched {
		totalEst += n.EstimatedMonthlyCost
		totalActual += n.ActualCost
	}
	nsRec := reconcileNamespaces(namespaces, totalEst, totalActual, nil, periodDays)
	if len(nsRec) != 2 {
		t.Fatalf("expected 2 namespace reconciliations, got %d", len(nsRec))
	}
	for _, ns := range nsRec {
		if ns.HasSplitCost {
			t.Errorf("Azure should NOT have split cost, but %s has it", ns.Name)
		}
		if ns.ActualCost <= 0 {
			t.Errorf("namespace %s actual cost should be >0 (proportional), got %.2f", ns.Name, ns.ActualCost)
		}
	}

	// Verify coverage gaps: only on-demand nodes with >$50 actual
	coverageGaps := DetectCoverageGaps(matched)
	for _, g := range coverageGaps {
		if g.MonthlyCost < 50 {
			t.Errorf("coverage gap %s has cost %.2f, should be >$50", g.NodeName, g.MonthlyCost)
		}
	}
}

func TestReconcileAzureMixedPricingTerms(t *testing.T) {
	// Test that RI, SP, Spot savings are correctly tallied from Azure results
	nodes := []collector.NodeInfo{
		{Name: "ri-node", ProviderID: "azure:///subscriptions/s/rg/providers/Microsoft.Compute/virtualMachines/ri-node", InstanceType: "Standard_D4s_v3"},
		{Name: "sp-node", ProviderID: "azure:///subscriptions/s/rg/providers/Microsoft.Compute/virtualMachines/sp-node", InstanceType: "Standard_D4s_v3"},
		{Name: "spot-node", ProviderID: "azure:///subscriptions/s/rg/providers/Microsoft.Compute/virtualMachines/spot-node", InstanceType: "Standard_D4s_v3", IsSpot: true},
		{Name: "od-node", ProviderID: "azure:///subscriptions/s/rg/providers/Microsoft.Compute/virtualMachines/od-node", InstanceType: "Standard_D4s_v3"},
	}

	estimated := map[string]float64{
		"ri-node":   140, // on-demand estimate
		"sp-node":   140,
		"spot-node": 140,
		"od-node":   140,
	}

	// Monthly projected: raw / 7 * 30.42
	// RI: 10/7*30.42 = $43.46 actual vs $140 est → savings
	// SP: 15/7*30.42 = $65.19 actual vs $140 est → savings
	// Spot: 5/7*30.42 = $21.73 actual vs $140 est → savings
	// OD: 35/7*30.42 = $152.10 actual vs $140 est → no savings (actual > est)
	curCosts := map[string]*AggregatedCost{
		"ri-node":   {ResourceID: "ri-node", TotalCost: 10, ComputeCost: 10, PricingTerm: "Reserved", RICost: 10},
		"sp-node":   {ResourceID: "sp-node", TotalCost: 15, ComputeCost: 15, PricingTerm: "SavingsPlan", SPCost: 15},
		"spot-node": {ResourceID: "spot-node", TotalCost: 5, ComputeCost: 5, PricingTerm: "Spot", SpotCost: 5},
		"od-node":   {ResourceID: "od-node", TotalCost: 35, ComputeCost: 35, PricingTerm: "OnDemand", OnDemandCost: 35},
	}

	matched, _, _ := MatchNodesToCUR(nodes, estimated, curCosts, 7, time.Time{})

	var riCount, spCount, spotCount, odCount int
	var riSavings, spSavings, spotSavings float64

	for _, n := range matched {
		switch n.PricingTerm {
		case "Reserved":
			riCount++
		case "SavingsPlan":
			spCount++
		case "Spot":
			spotCount++
		case "OnDemand":
			odCount++
		}
		saving := n.EstimatedMonthlyCost - n.ActualCost
		if saving <= 0 {
			continue
		}
		computeTotal := n.OnDemandCost + n.SPCost + n.RICost + n.SpotCost
		if computeTotal > 0 {
			riSavings += saving * n.RICost / computeTotal
			spSavings += saving * n.SPCost / computeTotal
			spotSavings += saving * n.SpotCost / computeTotal
		}
	}

	if riCount != 1 {
		t.Errorf("RI count = %d, want 1", riCount)
	}
	if spCount != 1 {
		t.Errorf("SP count = %d, want 1", spCount)
	}
	if spotCount != 1 {
		t.Errorf("Spot count = %d, want 1", spotCount)
	}
	if odCount != 1 {
		t.Errorf("OD count = %d, want 1", odCount)
	}
	if riSavings <= 0 {
		t.Errorf("RI savings should be >0, got %.2f", riSavings)
	}
	if spSavings <= 0 {
		t.Errorf("SP savings should be >0, got %.2f", spSavings)
	}
	if spotSavings <= 0 {
		t.Errorf("Spot savings should be >0, got %.2f", spotSavings)
	}
}

func TestPartialSPCoverageAttributesSavings(t *testing.T) {
	nodes := []collector.NodeInfo{
		{Name: "node-0", ProviderID: "azure:///subscriptions/s/rg/providers/Microsoft.Compute/virtualMachines/node-0", InstanceType: "Standard_D2s_v3"},
	}
	estimated := map[string]float64{"node-0": 87.60}

	// Node with 95% OnDemand + 5% SP — mimics real $0.01/hr SP on a $0.12/hr node.
	// Even with partial coverage, PricingTerm should be "SavingsPlan" because the node
	// IS covered by SP (the OnDemand portion is pre-SP historical cost in the billing window).
	curCosts := map[string]*AggregatedCost{
		"node-0": {
			ResourceID:   "node-0",
			TotalCost:    19.5,
			ComputeCost:  19.5,
			PricingTerm:  "SavingsPlan",
			OnDemandCost: 18.5,
			SPCost:       1.0,
		},
	}

	matched, _, _ := MatchNodesToCUR(nodes, estimated, curCosts, 7, time.Time{})
	if len(matched) != 1 {
		t.Fatalf("expected 1 matched node, got %d", len(matched))
	}

	n := matched[0]
	if n.PricingTerm != "SavingsPlan" {
		t.Errorf("PricingTerm = %s, want SavingsPlan (node has SP coverage)", n.PricingTerm)
	}
	if n.SPCost <= 0 {
		t.Errorf("SPCost should be >0, got %.2f", n.SPCost)
	}
	if n.OnDemandCost <= 0 {
		t.Errorf("OnDemandCost should be >0 (partial coverage), got %.2f", n.OnDemandCost)
	}

	// Savings should be attributed proportionally
	saving := n.EstimatedMonthlyCost - n.ActualCost
	computeTotal := n.OnDemandCost + n.SPCost
	spSavings := saving * n.SPCost / computeTotal

	if spSavings <= 0 {
		t.Errorf("SP-attributed savings should be >0, got %.4f", spSavings)
	}
	if spSavings >= saving {
		t.Errorf("SP savings (%.2f) should be less than total saving (%.2f)", spSavings, saving)
	}

	// Node with partial SP should NOT appear as a coverage gap
	gaps := DetectCoverageGaps(matched)
	for _, g := range gaps {
		if g.NodeName == "node-0" {
			t.Error("node with partial SP coverage should not be a coverage gap")
		}
	}
}

func TestReconcileAzureVMSSMultiPool(t *testing.T) {
	// Two different VMSS pools — each should match independently
	nodes := []collector.NodeInfo{
		{Name: "pool1-000", ProviderID: "azure:///subscriptions/s/rg/providers/Microsoft.Compute/virtualMachineScaleSets/aks-pool1-vmss/virtualMachines/0"},
		{Name: "pool1-001", ProviderID: "azure:///subscriptions/s/rg/providers/Microsoft.Compute/virtualMachineScaleSets/aks-pool1-vmss/virtualMachines/1"},
		{Name: "pool2-000", ProviderID: "azure:///subscriptions/s/rg/providers/Microsoft.Compute/virtualMachineScaleSets/aks-pool2-vmss/virtualMachines/0"},
		{Name: "pool2-001", ProviderID: "azure:///subscriptions/s/rg/providers/Microsoft.Compute/virtualMachineScaleSets/aks-pool2-vmss/virtualMachines/1"},
		{Name: "pool2-002", ProviderID: "azure:///subscriptions/s/rg/providers/Microsoft.Compute/virtualMachineScaleSets/aks-pool2-vmss/virtualMachines/2"},
	}

	estimated := map[string]float64{
		"pool1-000": 100, "pool1-001": 100,
		"pool2-000": 100, "pool2-001": 100, "pool2-002": 100,
	}

	curCosts := map[string]*AggregatedCost{
		"aks-pool1-vmss": {ResourceID: "aks-pool1-vmss", TotalCost: 140, ComputeCost: 140, PricingTerm: "Reserved", RICost: 140},
		"aks-pool2-vmss": {ResourceID: "aks-pool2-vmss", TotalCost: 300, ComputeCost: 300, PricingTerm: "OnDemand", OnDemandCost: 300},
	}

	matched, unmatchedCUR, unmatchedNodes := MatchNodesToCUR(nodes, estimated, curCosts, 7, time.Time{})

	if len(matched) != 5 {
		t.Fatalf("expected 5 matched, got %d", len(matched))
	}
	if unmatchedCUR != 0 || unmatchedNodes != 0 {
		t.Errorf("unmatched: CUR=%d nodes=%d, want 0/0", unmatchedCUR, unmatchedNodes)
	}

	// pool1: $140 / 2 nodes = $70 each
	// pool2: $300 / 3 nodes = $100 each
	for _, n := range matched {
		var wantRaw float64
		switch {
		case n.NodeName == "pool1-000" || n.NodeName == "pool1-001":
			wantRaw = 70
			if n.PricingTerm != "Reserved" {
				t.Errorf("%s: term = %s, want Reserved", n.NodeName, n.PricingTerm)
			}
		case n.NodeName == "pool2-000" || n.NodeName == "pool2-001" || n.NodeName == "pool2-002":
			wantRaw = 100
			if n.PricingTerm != "OnDemand" {
				t.Errorf("%s: term = %s, want OnDemand", n.NodeName, n.PricingTerm)
			}
		}
		wantMonthly := wantRaw / 7 * DaysPerMonth
		if math.Abs(n.ActualCost-wantMonthly) > 0.01 {
			t.Errorf("%s: actual = %.2f, want %.2f", n.NodeName, n.ActualCost, wantMonthly)
		}
	}
}

func TestReconcileAzurePartialPeriodDrift(t *testing.T) {
	// Node created during billing period should get partial-period drift alert, not pricing alert.
	// Actual cost for partial uptime will be low, projected monthly will be much lower than estimate.
	createdDuringPeriod := time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC)
	periodStart := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	nodes := []collector.NodeInfo{
		{Name: "new-node", ProviderID: "azure:///subscriptions/s/rg/providers/Microsoft.Compute/virtualMachines/new-node", CreatedAt: createdDuringPeriod},
	}

	// Raw cost is low because node only existed ~3 days of 7.
	// Monthly projection: 10/7*30.42 = $43.46 vs $140 est → -69% diff → triggers <-30% alert.
	// But because createdAt is after periodStart, it gets partial-period message instead.
	curCosts := map[string]*AggregatedCost{
		"new-node": {ResourceID: "new-node", TotalCost: 10, ComputeCost: 10, PricingTerm: "OnDemand", OnDemandCost: 10},
	}

	estimated := map[string]float64{"new-node": 140}

	results, _, _ := MatchNodesToCUR(nodes, estimated, curCosts, 7, periodStart)

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].DriftAlert == "" {
		t.Error("expected partial period drift alert")
	}
	if !strings.Contains(results[0].DriftAlert, "partial uptime") {
		t.Errorf("drift alert should mention partial uptime, got: %s", results[0].DriftAlert)
	}
}
