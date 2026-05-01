package pricing

import (
	"context"
	"strings"

	"github.com/tanrikuluozlem/burn/internal/collector"
)

type StaticProvider struct {
	prices map[string]map[string]float64 // region -> instanceType -> price
}

func NewStaticProvider() *StaticProvider {
	return &StaticProvider{
		prices: defaultPrices(),
	}
}

func (p *StaticProvider) GetHourlyPrice(_ context.Context, instanceType, region string, isSpot bool) (float64, error) {
	regionPrices, ok := p.prices[region]
	if !ok {
		regionPrices = p.prices["us-east-1"] // fallback to us-east-1 prices
	}

	price, ok := regionPrices[instanceType]
	if !ok {
		// Try to estimate price based on instance family
		price = estimatePrice(instanceType)
	}

	if isSpot {
		// Spot discount (~79% off on-demand)
		return price * 0.21, nil
	}
	return price, nil
}

// estimatePrice returns a rough estimate for unknown instance types based on naming patterns
func estimatePrice(instanceType string) float64 {
	// Check sizes from largest to smallest to match correctly (e.g., "2xlarge" before "xlarge")
	sizes := []struct {
		suffix string
		price  float64
	}{
		{"8xlarge", 1.60},
		{"4xlarge", 0.80},
		{"2xlarge", 0.40},
		{"xlarge", 0.20},
		{"large", 0.10},
		{"medium", 0.04},
		{"small", 0.02},
		{"micro", 0.01},
		{"nano", 0.005},
	}

	for _, s := range sizes {
		if strings.HasSuffix(instanceType, s.suffix) {
			return s.price
		}
	}

	// Default fallback for completely unknown types
	return 0.10
}

func (p *StaticProvider) GetHourlyPriceForNode(ctx context.Context, node collector.NodeInfo) (float64, error) {
	return p.GetHourlyPrice(ctx, node.InstanceType, node.Region, node.IsSpot)
}

var storagePrices = map[string]float64{
	// AWS EBS
	"gp2": 0.10, "gp3": 0.08, "io1": 0.125, "io2": 0.125, "st1": 0.045, "sc1": 0.015,
	// GCP Persistent Disk
	"pd-standard": 0.04, "pd-ssd": 0.17, "pd-balanced": 0.10,
	// Azure Managed Disk
	"Premium_LRS": 0.135, "StandardSSD_LRS": 0.075, "Standard_LRS": 0.04,
	"managed-premium": 0.135, "managed": 0.04, "default": 0.04,
}

func (p *StaticProvider) GetStoragePricePerGiBMonth(storageClass string) float64 {
	if price, ok := storagePrices[storageClass]; ok {
		return price
	}
	return storagePrices["default"]
}

func (p *StaticProvider) GetLoadBalancerPricePerHour() float64 {
	return 0.025 // AWS ALB/NLB
}

func (p *StaticProvider) GetNetworkEgressPricePerGiB() float64 {
	return 0.01 // zone egress
}

func (p *StaticProvider) GetNodePricing(ctx context.Context, node collector.NodeInfo) (*NodePricing, error) {
	hourly, err := p.GetHourlyPriceForNode(ctx, node)
	if err != nil {
		return nil, err
	}
	cpuPerCore, ramPerGiB := SplitNodeCost(hourly, node.CPUAllocatable, node.MemAllocatable)
	return &NodePricing{
		HourlyTotal:    hourly,
		CPUCostPerCore: cpuPerCore,
		RAMCostPerGiB:  ramPerGiB,
	}, nil
}

func defaultPrices() map[string]map[string]float64 {
	// Prices as of April 2026 - Linux On-Demand
	return map[string]map[string]float64{
		"us-east-1": {
			// T3 family (burstable)
			"t3.micro": 0.0104, "t3.small": 0.0208, "t3.medium": 0.0416,
			"t3.large": 0.0832, "t3.xlarge": 0.1664, "t3.2xlarge": 0.3328,
			// T3a family (AMD)
			"t3a.micro": 0.0094, "t3a.small": 0.0188, "t3a.medium": 0.0376,
			"t3a.large": 0.0752, "t3a.xlarge": 0.1504, "t3a.2xlarge": 0.3008,
			// M5 family (general purpose)
			"m5.large": 0.096, "m5.xlarge": 0.192, "m5.2xlarge": 0.384, "m5.4xlarge": 0.768,
			// M6i family (latest gen)
			"m6i.large": 0.096, "m6i.xlarge": 0.192, "m6i.2xlarge": 0.384,
			// C5 family (compute optimized)
			"c5.large": 0.085, "c5.xlarge": 0.17, "c5.2xlarge": 0.34, "c5.4xlarge": 0.68,
			// C6i family (latest gen)
			"c6i.large": 0.085, "c6i.xlarge": 0.17, "c6i.2xlarge": 0.34,
			// R5 family (memory optimized)
			"r5.large": 0.126, "r5.xlarge": 0.252, "r5.2xlarge": 0.504,
		},
		"us-west-2": {
			"t3.micro": 0.0104, "t3.small": 0.0208, "t3.medium": 0.0416, "t3.large": 0.0832,
			"t3a.micro": 0.0094, "t3a.small": 0.0188, "t3a.medium": 0.0376, "t3a.large": 0.0752,
			"m5.large": 0.096, "m5.xlarge": 0.192, "m5.2xlarge": 0.384,
			"m6i.large": 0.096, "m6i.xlarge": 0.192,
			"c5.large": 0.085, "c5.xlarge": 0.17, "c5.2xlarge": 0.34,
		},
		"eu-west-1": {
			"t3.micro": 0.0114, "t3.small": 0.0228, "t3.medium": 0.0456, "t3.large": 0.0912,
			"t3a.micro": 0.0102, "t3a.small": 0.0205, "t3a.medium": 0.0410, "t3a.large": 0.0820,
			"m5.large": 0.107, "m5.xlarge": 0.214, "m5.2xlarge": 0.428,
			"m6i.large": 0.107, "m6i.xlarge": 0.214,
			"c5.large": 0.096, "c5.xlarge": 0.192, "c5.2xlarge": 0.384,
		},
		"eu-central-1": {
			"t3.micro": 0.0116, "t3.small": 0.0232, "t3.medium": 0.0464, "t3.large": 0.0928, "t3.xlarge": 0.1856,
			"t3a.micro": 0.0104, "t3a.small": 0.0209, "t3a.medium": 0.0418, "t3a.large": 0.0835,
			"m5.large": 0.107, "m5.xlarge": 0.214, "m5.2xlarge": 0.428,
			"m6i.large": 0.107, "m6i.xlarge": 0.214,
			"c5.large": 0.096, "c5.xlarge": 0.192, "c5.2xlarge": 0.384,
			"r5.large": 0.141, "r5.xlarge": 0.282,
		},
		"ap-northeast-1": { // Tokyo
			"t3.micro": 0.0136, "t3.small": 0.0272, "t3.medium": 0.0544, "t3.large": 0.1088,
			"m5.large": 0.124, "m5.xlarge": 0.248,
			"c5.large": 0.107, "c5.xlarge": 0.214,
		},
		"ap-southeast-1": { // Singapore
			"t3.micro": 0.0132, "t3.small": 0.0264, "t3.medium": 0.0528, "t3.large": 0.1056,
			"m5.large": 0.120, "m5.xlarge": 0.240,
			"c5.large": 0.102, "c5.xlarge": 0.204,
		},
	}
}
