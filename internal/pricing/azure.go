package pricing

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const azurePricingAPI = "https://prices.azure.com/api/retail/prices"

type AzureProvider struct {
	client *http.Client
	cache  map[string]cachedPrice
	mu     sync.RWMutex
}

func NewAzureProvider() *AzureProvider {
	return &AzureProvider{
		client: &http.Client{Timeout: 10 * time.Second},
		cache:  make(map[string]cachedPrice),
	}
}

func (p *AzureProvider) GetHourlyPrice(ctx context.Context, vmSize, region string, isSpot bool) (float64, error) {
	key := fmt.Sprintf("%s:%s:%v", vmSize, region, isSpot)

	p.mu.RLock()
	if c, ok := p.cache[key]; ok && time.Now().Before(c.expiresAt) {
		p.mu.RUnlock()
		return c.price, nil
	}
	p.mu.RUnlock()

	price, err := p.fetchPrice(ctx, vmSize, region, isSpot)
	if err != nil {
		return 0, err
	}

	p.mu.Lock()
	if c, ok := p.cache[key]; ok && time.Now().Before(c.expiresAt) {
		p.mu.Unlock()
		return c.price, nil
	}
	p.cache[key] = cachedPrice{price: price, expiresAt: time.Now().Add(time.Hour)}
	p.mu.Unlock()
	return price, nil
}

func (p *AzureProvider) fetchPrice(ctx context.Context, vmSize, region string, isSpot bool) (float64, error) {
	priceType := "Consumption"
	if isSpot {
		priceType = "Spot"
	}

	filter := fmt.Sprintf(
		"serviceName eq 'Virtual Machines' and armRegionName eq '%s' and armSkuName eq '%s' and priceType eq '%s'",
		region, vmSize, priceType,
	)
	reqURL := fmt.Sprintf("%s?$filter=%s", azurePricingAPI, url.QueryEscape(filter))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return 0, err
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("azure: %d", resp.StatusCode)
	}

	var result azurePricingResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, err
	}

	// Find the correct price: Linux, not Low Priority, not Spot (unless we want Spot)
	for _, item := range result.Items {
		if item.ProductName == "" || isWindowsProduct(item.ProductName) {
			continue
		}
		// For on-demand, we want the base SKU (no "Low Priority" or "Spot" in skuName)
		// For spot, we want "Spot" in skuName
		if isSpot {
			if containsSpot(item.SkuName) {
				return item.RetailPrice, nil
			}
		} else {
			if !containsSpot(item.SkuName) && !containsLowPriority(item.SkuName) {
				return item.RetailPrice, nil
			}
		}
	}
	return 0, fmt.Errorf("no pricing for %s in %s", vmSize, region)
}

type azurePricingResponse struct {
	Items []azurePriceItem `json:"Items"`
}

type azurePriceItem struct {
	RetailPrice     float64 `json:"retailPrice"`
	UnitPrice       float64 `json:"unitPrice"`
	ProductName     string  `json:"productName"`
	SkuName         string  `json:"skuName"`
	MeterName       string  `json:"meterName"`
	ReservationTerm string  `json:"reservationTerm"`
}

func (p *AzureProvider) GetDiskPrice(ctx context.Context, diskType, region string) (float64, error) {
	key := fmt.Sprintf("disk:%s:%s", diskType, region)

	p.mu.RLock()
	if c, ok := p.cache[key]; ok && time.Now().Before(c.expiresAt) {
		p.mu.RUnlock()
		return c.price, nil
	}
	p.mu.RUnlock()

	// Map storage class to Azure meter name
	meterFilter := azureDiskMeter(diskType)

	filter := fmt.Sprintf(
		"serviceName eq 'Storage' and armRegionName eq '%s' and meterName eq '%s' and priceType eq 'Consumption'",
		region, meterFilter,
	)
	reqURL := fmt.Sprintf("%s?$filter=%s", azurePricingAPI, url.QueryEscape(filter))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return 0, err
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("azure disk: %d", resp.StatusCode)
	}

	var result azurePricingResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, err
	}

	tierGiB := azureDiskTierGiB(meterFilter)
	for _, item := range result.Items {
		if item.RetailPrice > 0 {
			// Azure managed disks are priced per-tier (fixed monthly), not per-GiB.
			// Convert to per-GiB rate so the caller can multiply by actual disk size.
			perGiB := item.RetailPrice / tierGiB
			p.mu.Lock()
			p.cache[key] = cachedPrice{price: perGiB, expiresAt: time.Now().Add(time.Hour)}
			p.mu.Unlock()
			return perGiB, nil
		}
	}
	return 0, fmt.Errorf("no disk pricing for %s in %s", diskType, region)
}

// azureDiskMeter maps a K8s storage class to the Azure Retail Prices API meter name.
func azureDiskMeter(diskType string) string {
	meters := map[string]string{
		"Premium_LRS":         "P10 LRS Disk",
		"StandardSSD_LRS":     "E10 LRS Disk",
		"Standard_LRS":        "S10 LRS Disk",
		"managed-premium":     "P10 LRS Disk",
		"managed-csi-premium": "P10 LRS Disk",
		"managed-csi":         "E10 LRS Disk",
		"managed":             "S10 LRS Disk",
	}
	if m, ok := meters[diskType]; ok {
		return m
	}
	return "E10 LRS Disk"
}

// azureDiskTierGiB returns the capacity of an Azure managed disk tier.
func azureDiskTierGiB(meter string) float64 {
	tiers := map[string]float64{
		"P4 LRS Disk":  32, "P6 LRS Disk": 64, "P10 LRS Disk": 128,
		"P15 LRS Disk": 256, "P20 LRS Disk": 512,
		"E4 LRS Disk":  32, "E6 LRS Disk": 64, "E10 LRS Disk": 128,
		"E15 LRS Disk": 256, "E20 LRS Disk": 512,
		"S4 LRS Disk":  32, "S6 LRS Disk": 64, "S10 LRS Disk": 128,
		"S15 LRS Disk": 256, "S20 LRS Disk": 512,
	}
	if g, ok := tiers[meter]; ok {
		return g
	}
	return 128
}

// GetRIMonthlyPrice returns the monthly cost for a 1-year RI.
// Azure returns the total term cost, so we divide by 12.
func (p *AzureProvider) GetRIMonthlyPrice(ctx context.Context, vmSize, region string) (float64, error) {
	key := fmt.Sprintf("ri1yr:%s:%s", vmSize, region)

	p.mu.RLock()
	if c, ok := p.cache[key]; ok && time.Now().Before(c.expiresAt) {
		p.mu.RUnlock()
		return c.price, nil
	}
	p.mu.RUnlock()

	filter := fmt.Sprintf(
		"serviceName eq 'Virtual Machines' and armRegionName eq '%s' and armSkuName eq '%s' and priceType eq 'Reservation' and reservationTerm eq '1 Year'",
		region, vmSize,
	)
	reqURL := fmt.Sprintf("%s?$filter=%s", azurePricingAPI, url.QueryEscape(filter))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return 0, err
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("azure RI pricing: %d", resp.StatusCode)
	}

	var result azurePricingResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, err
	}

	for _, item := range result.Items {
		if item.ProductName == "" || isWindowsProduct(item.ProductName) {
			continue
		}
		if item.RetailPrice > 0 && item.ReservationTerm == "1 Year" {
			monthly := item.RetailPrice / 12
			p.mu.Lock()
			p.cache[key] = cachedPrice{price: monthly, expiresAt: time.Now().Add(time.Hour)}
			p.mu.Unlock()
			return monthly, nil
		}
	}
	return 0, fmt.Errorf("no 1yr RI pricing for %s in %s", vmSize, region)
}

func isWindowsProduct(name string) bool {
	return strings.HasSuffix(name, "Windows")
}

func containsSpot(skuName string) bool {
	return strings.Contains(skuName, "Spot")
}

func containsLowPriority(skuName string) bool {
	return strings.Contains(skuName, "Low Priority")
}
