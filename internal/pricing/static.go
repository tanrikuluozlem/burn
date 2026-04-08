package pricing

import (
	"context"
	"fmt"
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
		regionPrices = p.prices["us-east-1"] // fallback
	}

	price, ok := regionPrices[instanceType]
	if !ok {
		return 0, fmt.Errorf("instance type %q not in price list for region %s", instanceType, region)
	}

	if isSpot {
		// Spot instances typically cost 30-50% of on-demand price (50-70% discount)
		// Using 0.35 as conservative estimate (65% discount)
		return price * 0.35, nil
	}
	return price, nil
}

func defaultPrices() map[string]map[string]float64 {
	return map[string]map[string]float64{
		"us-east-1": {
			"t3.micro":    0.0104,
			"t3.small":    0.0208,
			"t3.medium":   0.0416,
			"t3.large":    0.0832,
			"t3.xlarge":   0.1664,
			"t3.2xlarge":  0.3328,
			"m5.large":    0.096,
			"m5.xlarge":   0.192,
			"m5.2xlarge":  0.384,
			"m5.4xlarge":  0.768,
			"c5.large":    0.085,
			"c5.xlarge":   0.17,
			"c5.2xlarge":  0.34,
			"r5.large":    0.126,
			"r5.xlarge":   0.252,
		},
		"us-west-2": {
			"t3.micro":   0.0104,
			"t3.small":   0.0208,
			"t3.medium":  0.0416,
			"t3.large":   0.0832,
			"m5.large":   0.096,
			"m5.xlarge":  0.192,
			"c5.large":   0.085,
			"c5.xlarge":  0.17,
		},
		"eu-west-1": {
			"t3.micro":   0.0114,
			"t3.small":   0.0228,
			"t3.medium":  0.0456,
			"t3.large":   0.0912,
			"m5.large":   0.107,
			"m5.xlarge":  0.214,
			"c5.large":   0.096,
			"c5.xlarge":  0.192,
		},
		"eu-central-1": {
			"t3.micro":   0.0116,
			"t3.small":   0.0232,
			"t3.medium":  0.0464,
			"t3.large":   0.0928,
			"t3.xlarge":  0.1856,
			"m5.large":   0.107,
			"m5.xlarge":  0.214,
			"c5.large":   0.096,
			"c5.xlarge":  0.192,
		},
	}
}
