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

	// Spot: cloud API first, then embedded DB fallback
	if node.IsSpot {
		switch node.CloudProvider {
		case collector.CloudAWS:
			if p.aws != nil {
				if price, err := p.aws.GetHourlyPrice(ctx, node.InstanceType, node.Region, true); err == nil {
					return price, nil
				}
			}
		case collector.CloudAzure:
			if p.azure != nil {
				if price, err := p.azure.GetHourlyPrice(ctx, node.InstanceType, node.Region, true); err == nil {
					return price, nil
				}
			}
		}
		// Fallback: on-demand price * 0.21
		if cloudName != "" {
			if price, err := GetEmbeddedPrice(cloudName, node.Region, node.InstanceType); err == nil {
				return price * 0.21, nil
			}
		}
		return p.fallback.GetHourlyPrice(ctx, node.InstanceType, node.Region, true)
	}

	// On-demand: cloud API → embedded DB → static fallback
	switch node.CloudProvider {
	case collector.CloudAWS:
		if p.aws != nil {
			if price, err := p.aws.GetHourlyPrice(ctx, node.InstanceType, node.Region, false); err == nil {
				return price, nil
			}
		}
	case collector.CloudAzure:
		if p.azure != nil {
			if price, err := p.azure.GetHourlyPrice(ctx, node.InstanceType, node.Region, false); err == nil {
				return price, nil
			}
		}
	}

	// Embedded DB fallback
	if cloudName != "" {
		if price, err := GetEmbeddedPrice(cloudName, node.Region, node.InstanceType); err == nil {
			return price, nil
		}
	}

	// Static fallback
	return p.fallback.GetHourlyPrice(ctx, node.InstanceType, node.Region, false)
}

func (p *CloudPricingProvider) GetHourlyPrice(ctx context.Context, instanceType, region string, isSpot bool) (float64, error) {
	if isSpot {
		// Spot: cloud API first
		if p.aws != nil {
			if price, err := p.aws.GetHourlyPrice(ctx, instanceType, region, true); err == nil {
				return price, nil
			}
		}
		// Fallback: on-demand * 0.21
		if price, err := GetEmbeddedPrice("aws", region, instanceType); err == nil {
			return price * 0.21, nil
		}
		return p.fallback.GetHourlyPrice(ctx, instanceType, region, true)
	}

	// On-demand: cloud API → embedded DB → static fallback
	if p.aws != nil {
		if price, err := p.aws.GetHourlyPrice(ctx, instanceType, region, false); err == nil {
			return price, nil
		}
	}

	if price, err := GetEmbeddedPrice("aws", region, instanceType); err == nil {
		return price, nil
	}

	return p.fallback.GetHourlyPrice(ctx, instanceType, region, false)
}

func (p *CloudPricingProvider) GetNodePricing(ctx context.Context, node collector.NodeInfo) (*NodePricing, error) {
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
