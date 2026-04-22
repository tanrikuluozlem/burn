package pricing

import (
	"context"
	"log/slog"

	"github.com/tanrikuluozlem/burn/internal/collector"
)

type CloudPricingProvider struct {
	aws      *AWSProvider
	azure    *AzureProvider
	fallback *StaticProvider
}

func NewCloudPricingProvider(ctx context.Context) (*CloudPricingProvider, error) {
	aws, err := NewAWSProvider(ctx)
	if err != nil {
		slog.Debug("aws pricing unavailable, using fallback", "err", err)
	}
	return &CloudPricingProvider{
		aws:      aws,
		azure:    NewAzureProvider(),
		fallback: NewStaticProvider(),
	}, nil
}

func (p *CloudPricingProvider) GetHourlyPriceForNode(ctx context.Context, node collector.NodeInfo) (float64, error) {
	var cloudName string

	switch node.CloudProvider {
	case collector.CloudAWS:
		cloudName = "aws"
	case collector.CloudAzure:
		cloudName = "azure"
	case collector.CloudGCP:
		cloudName = "gcp"
	}

	// Try embedded DB first (fast, no API call)
	if cloudName != "" {
		if price, err := GetEmbeddedPrice(cloudName, node.Region, node.InstanceType); err == nil {
			if node.IsSpot {
				return price * 0.35, nil // spot ~65% discount
			}
			return price, nil
		}
	}

	// Fall back to cloud API
	switch node.CloudProvider {
	case collector.CloudAWS:
		if p.aws != nil {
			if price, err := p.aws.GetHourlyPrice(ctx, node.InstanceType, node.Region, node.IsSpot); err == nil {
				return price, nil
			}
		}

	case collector.CloudAzure:
		if p.azure != nil {
			if price, err := p.azure.GetHourlyPrice(ctx, node.InstanceType, node.Region, node.IsSpot); err == nil {
				return price, nil
			}
		}
	}

	// Last resort: static fallback with estimation
	return p.fallback.GetHourlyPrice(ctx, node.InstanceType, node.Region, node.IsSpot)
}

func (p *CloudPricingProvider) GetHourlyPrice(ctx context.Context, instanceType, region string, isSpot bool) (float64, error) {
	// Try embedded DB (AWS first since it's most common)
	if price, err := GetEmbeddedPrice("aws", region, instanceType); err == nil {
		if isSpot {
			return price * 0.35, nil
		}
		return price, nil
	}

	// Try cloud APIs
	if p.aws != nil {
		if price, err := p.aws.GetHourlyPrice(ctx, instanceType, region, isSpot); err == nil {
			return price, nil
		}
	}

	return p.fallback.GetHourlyPrice(ctx, instanceType, region, isSpot)
}
