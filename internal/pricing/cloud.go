package pricing

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ozlemtanrikulu/burn/internal/collector"
)

// CloudPricingProvider routes pricing requests to the appropriate cloud provider
type CloudPricingProvider struct {
	aws      *AWSProvider
	fallback *StaticProvider
}

func NewCloudPricingProvider(ctx context.Context) (*CloudPricingProvider, error) {
	aws, err := NewAWSProvider(ctx)
	if err != nil {
		slog.Warn("failed to initialize AWS pricing, using static fallback", "error", err)
	}

	return &CloudPricingProvider{
		aws:      aws,
		fallback: NewStaticProvider(),
	}, nil
}

func (p *CloudPricingProvider) GetHourlyPriceForNode(ctx context.Context, node collector.NodeInfo) (float64, error) {
	switch node.CloudProvider {
	case collector.CloudAWS:
		if p.aws != nil {
			price, err := p.aws.GetHourlyPrice(ctx, node.InstanceType, node.Region, node.IsSpot)
			if err == nil {
				slog.Debug("using AWS pricing API",
					"instance", node.InstanceType,
					"region", node.Region,
					"price", price,
				)
				return price, nil
			}
			slog.Warn("AWS pricing failed, using fallback",
				"instance", node.InstanceType,
				"region", node.Region,
				"error", err,
			)
		}
		// fallback to static
		price, err := p.fallback.GetHourlyPrice(ctx, node.InstanceType, node.Region, node.IsSpot)
		slog.Debug("using static pricing fallback",
			"instance", node.InstanceType,
			"region", node.Region,
			"price", price,
		)
		return price, err

	case collector.CloudGCP:
		// TODO: implement GCP pricing
		return p.fallback.GetHourlyPrice(ctx, node.InstanceType, node.Region, node.IsSpot)

	case collector.CloudAzure:
		// TODO: implement Azure pricing
		return p.fallback.GetHourlyPrice(ctx, node.InstanceType, node.Region, node.IsSpot)

	default:
		return 0, fmt.Errorf("unknown cloud provider for node %s", node.Name)
	}
}

// GetHourlyPrice implements Provider interface for backwards compatibility
func (p *CloudPricingProvider) GetHourlyPrice(ctx context.Context, instanceType, region string, isSpot bool) (float64, error) {
	// without cloud info, try AWS first then fallback
	if p.aws != nil {
		price, err := p.aws.GetHourlyPrice(ctx, instanceType, region, isSpot)
		if err == nil {
			return price, nil
		}
	}
	return p.fallback.GetHourlyPrice(ctx, instanceType, region, isSpot)
}
