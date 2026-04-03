package pricing

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ozlemtanrikulu/burn/internal/collector"
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
	switch node.CloudProvider {
	case collector.CloudAWS:
		if p.aws != nil {
			if price, err := p.aws.GetHourlyPrice(ctx, node.InstanceType, node.Region, node.IsSpot); err == nil {
				return price, nil
			}
		}
		return p.fallback.GetHourlyPrice(ctx, node.InstanceType, node.Region, node.IsSpot)

	case collector.CloudGCP:
		return p.fallback.GetHourlyPrice(ctx, node.InstanceType, node.Region, node.IsSpot)

	case collector.CloudAzure:
		if p.azure != nil {
			if price, err := p.azure.GetHourlyPrice(ctx, node.InstanceType, node.Region, node.IsSpot); err == nil {
				return price, nil
			}
		}
		return p.fallback.GetHourlyPrice(ctx, node.InstanceType, node.Region, node.IsSpot)

	default:
		return 0, fmt.Errorf("unknown cloud provider for node %s", node.Name)
	}
}

func (p *CloudPricingProvider) GetHourlyPrice(ctx context.Context, instanceType, region string, isSpot bool) (float64, error) {
	if p.aws != nil {
		if price, err := p.aws.GetHourlyPrice(ctx, instanceType, region, isSpot); err == nil {
			return price, nil
		}
	}
	return p.fallback.GetHourlyPrice(ctx, instanceType, region, isSpot)
}
