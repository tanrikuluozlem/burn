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
	RetailPrice float64 `json:"retailPrice"`
	UnitPrice   float64 `json:"unitPrice"`
	ProductName string  `json:"productName"`
	SkuName     string  `json:"skuName"`
	MeterName   string  `json:"meterName"`
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

	for _, item := range result.Items {
		if item.RetailPrice > 0 {
			p.mu.Lock()
			p.cache[key] = cachedPrice{price: item.RetailPrice, expiresAt: time.Now().Add(time.Hour)}
			p.mu.Unlock()
			return item.RetailPrice, nil
		}
	}
	return 0, fmt.Errorf("no disk pricing for %s in %s", diskType, region)
}

func azureDiskMeter(diskType string) string {
	meters := map[string]string{
		"Premium_LRS":     "P4 LRS Disk",
		"StandardSSD_LRS": "E4 LRS Disk",
		"Standard_LRS":    "S4 LRS Disk",
		"managed-premium": "P4 LRS Disk",
		"managed":         "S4 LRS Disk",
	}
	if m, ok := meters[diskType]; ok {
		return m
	}
	return "S4 LRS Disk"
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
