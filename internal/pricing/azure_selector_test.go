package pricing

import "testing"

func azureItem(price float64, product, sku string) AzurePriceItem {
	return AzurePriceItem{RetailPrice: price, ProductName: product, SkuName: sku, Type: "Consumption"}
}

func d2asV5Response() []AzurePriceItem {
	return []AzurePriceItem{
		azureItem(0.0784, "Dasv5 Series Cloud Services", "Standard_D2as_v5 Low Priority"),
		azureItem(0.019219, "Virtual Machines Dasv5 Series", "Standard_D2as_v5 Spot"),
		azureItem(0.196, "Dasv5 Series Cloud Services", "Standard_D2as_v5"),
		azureItem(0.036221, "Virtual Machines Dasv5 Series Windows", "Standard_D2as_v5 Spot"),
		azureItem(0.196, "Virtual Machines Dasv5 Series Windows", "Standard_D2as_v5"),
		azureItem(0.104, "Virtual Machines Dasv5 Series", "Standard_D2as_v5"),
		azureItem(0.0208, "Virtual Machines Dasv5 Series", "Standard_D2as_v5 Low Priority"),
		azureItem(0.0784, "Virtual Machines Dasv5 Series Windows", "Standard_D2as_v5 Low Priority"),
	}
}

func mustSelect(t *testing.T, items []AzurePriceItem, isSpot bool) float64 {
	t.Helper()
	price, err := SelectAzureVMPrice(items, isSpot)
	if err != nil {
		t.Fatalf("SelectAzureVMPrice() error = %v", err)
	}
	return price
}

func TestSelectAzureVMPrice_CloudServicesBeforeVM(t *testing.T) {
	if price := mustSelect(t, d2asV5Response(), false); price != 0.104 {
		t.Errorf("got %v, want 0.104 (0.196 is the Cloud Services price)", price)
	}
}

func TestSelectAzureVMPrice_ReversedOrderSameResult(t *testing.T) {
	items := d2asV5Response()
	reversed := make([]AzurePriceItem, 0, len(items))
	for i := len(items) - 1; i >= 0; i-- {
		reversed = append(reversed, items[i])
	}

	if forward, backward := mustSelect(t, items, false), mustSelect(t, reversed, false); forward != backward {
		t.Errorf("order changed the result: forward %v, reversed %v", forward, backward)
	}
}

func TestSelectAzureVMPrice_RejectsWindows(t *testing.T) {
	windows := azureItem(0.196, "Virtual Machines Dasv5 Series Windows", "Standard_D2as_v5")
	linux := azureItem(0.104, "Virtual Machines Dasv5 Series", "Standard_D2as_v5")

	if price := mustSelect(t, []AzurePriceItem{windows, linux}, false); price != 0.104 {
		t.Errorf("got %v, want 0.104", price)
	}
	if _, err := SelectAzureVMPrice([]AzurePriceItem{windows}, false); err == nil {
		t.Error("expected error when only a Windows product is present")
	}
}

func TestSelectAzureVMPrice_OnDemandAndSpot(t *testing.T) {
	items := d2asV5Response()

	if price := mustSelect(t, items, false); price != 0.104 {
		t.Errorf("on-demand = %v, want 0.104", price)
	}
	if price := mustSelect(t, items, true); price != 0.019219 {
		t.Errorf("spot = %v, want 0.019219", price)
	}
}

func TestSelectAzureVMPrice_RejectsLowPriority(t *testing.T) {
	lowPriority := azureItem(0.0208, "Virtual Machines Dasv5 Series", "Standard_D2as_v5 Low Priority")
	linux := azureItem(0.104, "Virtual Machines Dasv5 Series", "Standard_D2as_v5")

	if price := mustSelect(t, []AzurePriceItem{lowPriority, linux}, false); price != 0.104 {
		t.Errorf("got %v, want 0.104", price)
	}
	if _, err := SelectAzureVMPrice([]AzurePriceItem{lowPriority}, false); err == nil {
		t.Error("expected error for on-demand when only Low Priority is present")
	}
	if _, err := SelectAzureVMPrice([]AzurePriceItem{lowPriority}, true); err == nil {
		t.Error("Low Priority must not satisfy a spot request")
	}
}

func TestSelectAzureVMPrice_ShortSkuName(t *testing.T) {
	tests := []struct {
		product   string
		sku       string
		wantPrice float64
	}{
		{"Virtual Machines DSv3 Series", "D4s v3", 0.214},
		{"Virtual Machines Esv4 Series", "E2s v4", 0.126},
		{"Virtual Machines BS Series", "B2ms", 0.0992},
		{"Virtual Machines Dasv4 Series", "D4as v4", 0.214},
	}

	for _, tt := range tests {
		items := []AzurePriceItem{
			azureItem(tt.wantPrice, tt.product, tt.sku),
			azureItem(0.039547, tt.product, tt.sku+" Spot"),
			azureItem(0.398, tt.product+" Windows", tt.sku),
		}
		if price := mustSelect(t, items, false); price != tt.wantPrice {
			t.Errorf("%s/%s = %v, want %v", tt.product, tt.sku, price, tt.wantPrice)
		}
	}
}

func TestSelectAzureVMPrice_NonPrimaryMeterRegion(t *testing.T) {
	items := []AzurePriceItem{
		azureItem(0.043058, "Virtual Machines ESv3 Series Windows", "E2s v3 Spot"),
		azureItem(0.141, "Virtual Machines ESv3 Series", "E2s v3"),
		azureItem(0.233, "Virtual Machines ESv3 Series Windows", "E2s v3"),
		azureItem(0.0282, "Virtual Machines ESv3 Series", "E2s v3 Low Priority"),
		azureItem(0.026057, "Virtual Machines ESv3 Series", "E2s v3 Spot"),
	}

	if price := mustSelect(t, items, false); price != 0.141 {
		t.Errorf("got %v, want 0.141", price)
	}
}

func TestSelectAzureVMPrice_NoCandidates(t *testing.T) {
	reservation := AzurePriceItem{RetailPrice: 562, ProductName: "Virtual Machines Dasv5 Series", SkuName: "Standard_D2as_v5", Type: "Reservation"}

	tests := map[string][]AzurePriceItem{
		"empty":           {},
		"cloud services":  {azureItem(0.196, "Dasv5 Series Cloud Services", "Standard_D2as_v5")},
		"windows only":    {azureItem(0.196, "Virtual Machines Dasv5 Series Windows", "Standard_D2as_v5")},
		"not consumption": {reservation},
		"zero price":      {azureItem(0, "Virtual Machines Dasv5 Series", "Standard_D2as_v5")},
		"no product name": {azureItem(0.104, "", "Standard_D2as_v5")},
	}

	for name, items := range tests {
		t.Run(name, func(t *testing.T) {
			price, err := SelectAzureVMPrice(items, false)
			if err == nil {
				t.Fatalf("expected error, got %v", price)
			}
			if price != 0 {
				t.Errorf("error path returned price %v, want 0", price)
			}
		})
	}
}

func TestSelectAzureVMPrice_CloudServicesUnderVMPrefix(t *testing.T) {
	items := []AzurePriceItem{azureItem(0.196, "Virtual Machines Dasv5 Series Cloud Services", "Standard_D2as_v5")}

	price, err := SelectAzureVMPrice(items, false)
	if err == nil {
		t.Fatalf("expected error, got %v", price)
	}
	if price != 0 {
		t.Errorf("error path returned price %v, want 0", price)
	}
}

func TestSelectAzureVMPrice_AmbiguousPrices(t *testing.T) {
	items := []AzurePriceItem{
		azureItem(0.104, "Virtual Machines Dasv5 Series", "Standard_D2as_v5"),
		azureItem(0.111, "Virtual Machines Dasv5 Series", "Standard_D2as_v5"),
	}

	price, err := SelectAzureVMPrice(items, false)
	if err == nil {
		t.Fatalf("expected ambiguity error, got %v", price)
	}
	if price != 0 {
		t.Errorf("ambiguity returned price %v, want 0", price)
	}

	same := []AzurePriceItem{items[0], items[0]}
	if got := mustSelect(t, same, false); got != 0.104 {
		t.Errorf("got %v, want 0.104", got)
	}
}

func TestSelectAzureVMPrice_SpotAbsent(t *testing.T) {
	items := []AzurePriceItem{
		azureItem(0.0992, "Virtual Machines BS Series", "B2ms"),
		azureItem(0.107, "Virtual Machines BS Series Windows", "B2ms"),
	}

	price, err := SelectAzureVMPrice(items, true)
	if err == nil {
		t.Fatalf("expected error for a SKU with no spot price, got %v", price)
	}
	if price != 0 {
		t.Errorf("error path returned price %v, want 0", price)
	}
	if got := mustSelect(t, items, false); got != 0.0992 {
		t.Errorf("on-demand = %v, want 0.0992", got)
	}
}
