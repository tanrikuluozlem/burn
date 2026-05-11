package pricing

import (
	"context"
	"log/slog"

	"github.com/tanrikuluozlem/burn/internal/collector"
)

type CloudPricingProvider struct {
	aws           *AWSProvider
	azure         *AzureProvider
	fallback      *StaticProvider
	customPricing *CustomPricing
	detectedCloud collector.CloudProvider
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

func (p *CloudPricingProvider) SetCustomPricing(cp *CustomPricing) {
	p.customPricing = cp
}

func (p *CloudPricingProvider) GetHourlyPriceForNode(ctx context.Context, node collector.NodeInfo) (float64, error) {
	// Pass region to fallback for region-aware pricing
	if node.Region != "" {
		p.fallback.SetRegion(node.Region)
	}

	// Remember cloud provider for LB pricing
	if p.detectedCloud == "" && node.CloudProvider != collector.CloudUnknown {
		p.detectedCloud = node.CloudProvider
	}

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
			slog.Debug("using embedded pricing (cloud API unavailable)",
				"instance_type", node.InstanceType, "region", node.Region)
			return price, nil
		}
	}

	// Static fallback
	slog.Warn("using static fallback pricing (cloud API and embedded DB unavailable)",
		"instance_type", node.InstanceType, "region", node.Region)
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
	// Custom pricing override (on-prem)
	if p.customPricing != nil && node.CloudProvider == collector.CloudUnknown {
		np := &NodePricing{
			CPUCostPerCore: p.customPricing.CPUCostPerCoreHr,
			RAMCostPerGiB:  p.customPricing.RAMCostPerGiBHr,
			GPUCostPerUnit: p.customPricing.GPUCostPerHr,
		}
		cpuCores := float64(node.CPUAllocatable) / 1000.0
		ramGiB := float64(node.MemAllocatable) / (1024 * 1024 * 1024)
		np.HourlyTotal = np.CPUCostPerCore*cpuCores + np.RAMCostPerGiB*ramGiB + np.GPUCostPerUnit*float64(node.GPUCount)
		return np, nil
	}

	hourly, err := p.GetHourlyPriceForNode(ctx, node)
	if err != nil {
		return nil, err
	}

	if node.GPUCount > 0 {
		cpuPerCore, ramPerGiB, gpuPerUnit := SplitNodeCostWithGPU(hourly, node.CPUAllocatable, node.MemAllocatable, node.GPUCount)
		return &NodePricing{
			HourlyTotal:    hourly,
			CPUCostPerCore: cpuPerCore,
			RAMCostPerGiB:  ramPerGiB,
			GPUCostPerUnit: gpuPerUnit,
		}, nil
	}

	cpuPerCore, ramPerGiB := SplitNodeCost(hourly, node.CPUAllocatable, node.MemAllocatable)
	return &NodePricing{
		HourlyTotal:    hourly,
		CPUCostPerCore: cpuPerCore,
		RAMCostPerGiB:  ramPerGiB,
	}, nil
}

func (p *CloudPricingProvider) GetStoragePricePerGiBMonth(ctx context.Context, storageClass string) float64 {
	// Custom pricing override
	if p.customPricing != nil && p.customPricing.StoragePricePerGiBMo > 0 {
		return p.customPricing.StoragePricePerGiBMo
	}

	region := p.fallback.region

	// AWS EBS — API first
	if p.aws != nil && region != "" {
		price, err := p.aws.GetEBSPrice(ctx, storageClass, region)
		if err == nil && price > 0 {
			return price
		}
	}

	// Azure Managed Disk — API first
	if p.azure != nil && region != "" {
		price, err := p.azure.GetDiskPrice(ctx, storageClass, region)
		if err == nil && price > 0 {
			return price
		}
	}

	return p.fallback.GetStoragePricePerGiBMonth(ctx, storageClass)
}

func (p *CloudPricingProvider) GetLoadBalancerPricePerHour() float64 {
	switch p.detectedCloud {
	case collector.CloudAWS:
		// Try live API first, fallback to default
		if p.aws != nil && p.fallback.region != "" {
			if price, err := p.aws.GetLBPrice(context.Background(), p.fallback.region); err == nil {
				return price
			}
		}
		return 0.0225 // AWS ALB/NLB fallback (us-east-1)
	case collector.CloudAzure:
		return 0.005 // Azure Standard public IP (per-service cost in AKS shared LB)
	case collector.CloudGCP:
		return 0.025 // GCP forwarding rule
	default:
		return 0.0225 // default
	}
}

func (p *CloudPricingProvider) GetNetworkEgressPricePerGiB() float64 {
	return p.fallback.GetNetworkEgressPricePerGiB()
}
