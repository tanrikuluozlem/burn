package pricing

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
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

	for _, item := range result.Items {
		if item.ProductName != "" && !isWindowsProduct(item.ProductName) {
			return item.RetailPrice, nil
		}
	}
	if len(result.Items) > 0 {
		return result.Items[0].RetailPrice, nil
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

func isWindowsProduct(name string) bool {
	return len(name) > 7 && name[len(name)-7:] == "Windows"
}
