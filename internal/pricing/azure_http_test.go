package pricing

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/tanrikuluozlem/burn/internal/collector"
)

const (
	azureTestRegion = "uksouth"
	azureTestSKU    = "Standard_D4s_v5"
	azureTestLive   = 0.999
	azureTestEmbed  = 0.222
)

func azurePriceBody(linuxOnDemand float64) string {
	return fmt.Sprintf(`{"Items":[
		{"retailPrice":0.111,"productName":"Dsv5 Series Cloud Services","skuName":"Standard_D4s_v5","type":"Consumption"},
		{"retailPrice":0.406,"productName":"Virtual Machines Dsv5 Series Windows","skuName":"Standard_D4s_v5","type":"Consumption"},
		{"retailPrice":0.039538,"productName":"Virtual Machines Dsv5 Series","skuName":"Standard_D4s_v5 Spot","type":"Consumption"},
		{"retailPrice":0.0444,"productName":"Virtual Machines Dsv5 Series","skuName":"Standard_D4s_v5 Low Priority","type":"Consumption"},
		{"retailPrice":1234.0,"productName":"Virtual Machines Dsv5 Series","skuName":"Standard_D4s_v5","type":"Reservation","reservationTerm":"1 Year"},
		{"retailPrice":%g,"productName":"Virtual Machines Dsv5 Series","skuName":"Standard_D4s_v5","type":"Consumption"}
	]}`, linuxOnDemand)
}

func azureProviderAt(baseURL string) *AzureProvider {
	return &AzureProvider{
		client:  http.DefaultClient,
		baseURL: baseURL,
		cache:   make(map[string]cachedPrice),
	}
}

func azureCloudProviderAt(baseURL string) *CloudPricingProvider {
	return &CloudPricingProvider{
		azure:    azureProviderAt(baseURL),
		fallback: NewStaticProvider(),
	}
}

func azureTestNode() collector.NodeInfo {
	return collector.NodeInfo{
		InstanceType:  azureTestSKU,
		Region:        azureTestRegion,
		IsSpot:        false,
		CloudProvider: collector.CloudAzure,
	}
}

func TestAzureLiveAPIPriceUsedOverEmbedded(t *testing.T) {
	embedded, err := GetEmbeddedPrice("azure", azureTestRegion, azureTestSKU)
	if err != nil {
		t.Fatalf("GetEmbeddedPrice() error = %v", err)
	}
	if embedded != azureTestEmbed {
		t.Fatalf("embedded price = %v, want %v", embedded, azureTestEmbed)
	}

	var calls int32
	var gotFilter string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		gotFilter = r.URL.Query().Get("$filter")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, azurePriceBody(azureTestLive))
	}))
	defer srv.Close()

	price, err := azureCloudProviderAt(srv.URL).GetHourlyPriceForNode(context.Background(), azureTestNode())
	if err != nil {
		t.Fatalf("GetHourlyPriceForNode() error = %v", err)
	}

	if price != azureTestLive {
		t.Errorf("price = %v, want %v (live API)", price, azureTestLive)
	}
	if price == azureTestEmbed {
		t.Errorf("price = %v, which is the embedded catalog value — live API was not used", price)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("api calls = %d, want 1", n)
	}
	for _, want := range []string{
		"serviceName eq 'Virtual Machines'",
		"armRegionName eq '" + azureTestRegion + "'",
		"armSkuName eq '" + azureTestSKU + "'",
		"priceType eq 'Consumption'",
	} {
		if !strings.Contains(gotFilter, want) {
			t.Errorf("$filter missing %q, got %q", want, gotFilter)
		}
	}
}

func TestAzureAPIFailureFallsBackToEmbedded(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	price, err := azureCloudProviderAt(srv.URL).GetHourlyPriceForNode(context.Background(), azureTestNode())
	if err != nil {
		t.Fatalf("GetHourlyPriceForNode() error = %v", err)
	}

	if price != azureTestEmbed {
		t.Errorf("price = %v, want %v (embedded fallback)", price, azureTestEmbed)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("api calls = %d, want 1", n)
	}
}

func TestNewAzureProviderUsesProductionEndpoint(t *testing.T) {
	if got := NewAzureProvider().baseURL; got != azurePricingAPI {
		t.Errorf("baseURL = %q, want %q", got, azurePricingAPI)
	}
	if azurePricingAPI != "https://prices.azure.com/api/retail/prices" {
		t.Errorf("production endpoint changed: %q", azurePricingAPI)
	}
}
