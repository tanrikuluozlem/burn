package billing

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/costmanagement/armcostmanagement"
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

func TestParseAzureCostResultFiltersNonVM(t *testing.T) {
	rows := [][]any{
		{float64(100.0), "/subscriptions/s/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/node-1", "Microsoft.Compute/virtualMachines", "Virtual Machines"},
		{float64(50.0), "/subscriptions/s/resourceGroups/rg/providers/Microsoft.Storage/storageAccounts/sa1", "Microsoft.Storage/storageAccounts", "Storage"},
		{float64(5.0), "/subscriptions/s/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/node-1", "Microsoft.Compute/virtualMachines", "Networking"},
		// Azure returns "Bandwidth" for VMSS egress, not "Networking"
		{float64(0.003), "/subscriptions/s/resourceGroups/rg/providers/microsoft.compute/virtualmachinescalesets/aks-pool-vmss", "microsoft.compute/virtualmachinescalesets", "Bandwidth"},
	}

	result := buildMockUsageResponse(rows)
	items := parseAzureCostResult(result)

	if len(items) != 3 {
		t.Fatalf("expected 3 items (VM compute + VM networking + VMSS bandwidth), got %d", len(items))
	}

	if items[0].ResourceID != "node-1" {
		t.Errorf("first item resource = %s, want node-1", items[0].ResourceID)
	}
	if items[0].EffectiveCost != 100 {
		t.Errorf("first item cost = %f, want 100", items[0].EffectiveCost)
	}
	if items[0].UsageType != "BoxUsage" {
		t.Errorf("first item usage type = %s, want BoxUsage", items[0].UsageType)
	}
	if items[1].UsageType != "DataTransfer" {
		t.Errorf("second item usage type = %s, want DataTransfer", items[1].UsageType)
	}
	if items[2].UsageType != "DataTransfer" {
		t.Errorf("bandwidth item usage type = %s, want DataTransfer", items[2].UsageType)
	}
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

	if len(items) != 1 {
		t.Fatalf("expected 1 VM item, got %d", len(items))
	}
	if items[0].EffectiveCost != 5.76 {
		t.Errorf("cost = %f, want 5.76", items[0].EffectiveCost)
	}
	if items[0].ResourceID != "aks-nodepool1-32627587-vmss" {
		t.Errorf("resource = %s, want aks-nodepool1-32627587-vmss", items[0].ResourceID)
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
