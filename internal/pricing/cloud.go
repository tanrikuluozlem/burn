package pricing

import (
	"context"
	"log/slog"
	"sync"

	"github.com/tanrikuluozlem/burn/internal/collector"
)

type CloudPricingProvider struct {
	aws           *AWSProvider
	awsOnce       sync.Once
	azure         *AzureProvider
	fallback      *StaticProvider
	customPricing *CustomPricing
	cloudOnce     sync.Once
	detectedCloud collector.CloudProvider
}

func NewCloudPricingProvider(ctx context.Context) (*CloudPricingProvider, error) {
	return &CloudPricingProvider{
		azure:    NewAzureProvider(),
		fallback: NewStaticProvider(),
	}, nil
}

func (p *CloudPricingProvider) initAWS(ctx context.Context) {
	p.awsOnce.Do(func() {
		prov, err := NewAWSProvider(ctx)
		if err != nil {
			slog.Debug("aws pricing api unavailable", "err", err)
			return
		}
		p.aws = prov
	})
}

func (p *CloudPricingProvider) SetCustomPricing(cp *CustomPricing) {
	p.customPricing = cp
}

const (
	spotFallbackMultiplier = 0.21
	hoursPerMonth          = 730.0 // 365 * 24 / 12
)

func (p *CloudPricingProvider) detectCloud(node collector.NodeInfo) {
	p.cloudOnce.Do(func() {
		if node.CloudProvider != collector.CloudUnknown {
			p.detectedCloud = node.CloudProvider
		}
		if node.Region != "" {
			p.fallback.SetRegion(node.Region)
		}
	})
}

func (p *CloudPricingProvider) GetHourlyPriceForNode(ctx context.Context, node collector.NodeInfo) (float64, error) {
	p.detectCloud(node)
	return p.GetHourlyPrice(ctx, node.InstanceType, node.Region, node.IsSpot)
}

func (p *CloudPricingProvider) GetHourlyPrice(ctx context.Context, instanceType, region string, isSpot bool) (float64, error) {
	cloudName := ""
	switch p.detectedCloud {
	case collector.CloudAWS:
		cloudName = "aws"
	case collector.CloudAzure:
		cloudName = "azure"
	case collector.CloudGCP:
		cloudName = "gcp"
	}

	if isSpot {
		switch p.detectedCloud {
		case collector.CloudAWS:
			p.initAWS(ctx)
			if p.aws != nil {
				if price, err := p.aws.GetHourlyPrice(ctx, instanceType, region, true); err == nil {
					return price, nil
				}
			}
		case collector.CloudAzure:
			if price, err := p.azure.GetHourlyPrice(ctx, instanceType, region, true); err == nil {
				return price, nil
			}
		}
		if cloudName != "" {
			if price, err := GetEmbeddedPrice(cloudName, region, instanceType); err == nil {
				savings, _, ok := lookupSpotAdvisor(instanceType, region)
				if ok && savings > 0 {
					return price * (1 - float64(savings)/100.0), nil
				}
				return price * spotFallbackMultiplier, nil
			}
		}
		return p.fallback.GetHourlyPrice(ctx, instanceType, region, true)
	}

	switch p.detectedCloud {
	case collector.CloudAWS:
		p.initAWS(ctx)
		if p.aws != nil {
			if price, err := p.aws.GetHourlyPrice(ctx, instanceType, region, false); err == nil {
				return price, nil
			}
		}
	case collector.CloudAzure:
		if price, err := p.azure.GetHourlyPrice(ctx, instanceType, region, false); err == nil {
			return price, nil
		}
	}

	if cloudName != "" {
		if price, err := GetEmbeddedPrice(cloudName, region, instanceType); err == nil {
			return price, nil
		}
	}

	return p.fallback.GetHourlyPrice(ctx, instanceType, region, false)
}

func (p *CloudPricingProvider) GetNodePricing(ctx context.Context, node collector.NodeInfo) (*NodePricing, error) {
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
	if p.customPricing != nil && p.customPricing.StoragePricePerGiBMo > 0 {
		return p.customPricing.StoragePricePerGiBMo
	}

	region := p.fallback.region

	if region != "" {
		switch p.detectedCloud {
		case collector.CloudAWS:
			p.initAWS(ctx)
			if p.aws != nil {
				price, err := p.aws.GetEBSPrice(ctx, storageClass, region)
				if err == nil && price > 0 {
					return price
				}
			}
		case collector.CloudAzure:
			price, err := p.azure.GetDiskPrice(ctx, storageClass, region)
			if err == nil && price > 0 {
				return price
			}
		}
	}

	return p.fallback.GetStoragePricePerGiBMonth(ctx, storageClass)
}

func (p *CloudPricingProvider) GetLoadBalancerPricePerHour() float64 {
	switch p.detectedCloud {
	case collector.CloudAWS:
		p.initAWS(context.Background())
		if p.aws != nil && p.fallback.region != "" {
			if price, err := p.aws.GetLBPrice(context.Background(), p.fallback.region); err == nil {
				return price
			}
		}
		return 0.0225
	case collector.CloudAzure:
		return 0.005
	case collector.CloudGCP:
		return 0.025
	default:
		return 0.0225
	}
}

func (p *CloudPricingProvider) GetNetworkEgressPricePerGiB() float64 {
	return p.fallback.GetNetworkEgressPricePerGiB()
}

func (p *CloudPricingProvider) GetRISaving(ctx context.Context, instanceType, region string) (saving float64, pct float64, ok bool) {
	if region == "" {
		return 0, 0, false
	}
	switch p.detectedCloud {
	case collector.CloudAWS:
		p.initAWS(ctx)
		if p.aws == nil {
			return 0, 0, false
		}
		riHourly, err := p.aws.GetRIHourlyPrice(ctx, instanceType, region)
		if err != nil {
			return 0, 0, false
		}
		odHourly, err := p.aws.GetHourlyPrice(ctx, instanceType, region, false)
		if err != nil {
			return 0, 0, false
		}
		riMonthly := riHourly * hoursPerMonth
		odMonthly := odHourly * hoursPerMonth
		saving = odMonthly - riMonthly
		if saving > 0 {
			return saving, saving / odMonthly * 100, true
		}
	case collector.CloudAzure:
		riMonthly, err := p.azure.GetRIMonthlyPrice(ctx, instanceType, region)
		if err != nil {
			return 0, 0, false
		}
		odHourly, err := p.azure.GetHourlyPrice(ctx, instanceType, region, false)
		if err != nil {
			return 0, 0, false
		}
		odMonthly := odHourly * hoursPerMonth
		saving = odMonthly - riMonthly
		if saving > 0 {
			return saving, saving / odMonthly * 100, true
		}
	}
	return 0, 0, false
}

func (p *CloudPricingProvider) GetSpotDiscount(ctx context.Context, instanceType, region string) SpotDiscount {
	if region == "" {
		return SpotDiscount{Discount: 0, InterruptionRate: -1, Source: "unavailable"}
	}

	switch p.detectedCloud {
	case collector.CloudAWS:
		p.initAWS(ctx)
		if p.aws != nil {
			spotPrice, err := p.aws.GetHourlyPrice(ctx, instanceType, region, true)
			if err == nil && spotPrice > 0 {
				onDemandPrice, err := p.aws.GetHourlyPrice(ctx, instanceType, region, false)
				if err == nil && onDemandPrice > 0 {
					discount := 1 - (spotPrice / onDemandPrice)
					if discount > 0 && discount < 1 {
						_, ir, ok := lookupSpotAdvisor(instanceType, region)
						irVal := -1
						if ok {
							irVal = ir
						}
						return SpotDiscount{
							Discount:        discount,
							InterruptionRate: irVal,
							Source:          "api",
						}
					}
				}
			}
		}
	case collector.CloudAzure:
		spotPrice, err := p.azure.GetHourlyPrice(ctx, instanceType, region, true)
		if err == nil && spotPrice > 0 {
			onDemandPrice, err := p.azure.GetHourlyPrice(ctx, instanceType, region, false)
			if err == nil && onDemandPrice > 0 {
				discount := 1 - (spotPrice / onDemandPrice)
				if discount > 0 && discount < 1 {
					return SpotDiscount{
						Discount:        discount,
						InterruptionRate: -1,
						Source:          "api",
					}
				}
			}
		}
	}

	savings, ir, ok := lookupSpotAdvisor(instanceType, region)
	if ok && savings > 0 {
		return SpotDiscount{
			Discount:        float64(savings) / 100.0,
			InterruptionRate: ir,
			Source:          "advisor",
		}
	}

	return SpotDiscount{
		Discount:        0,
		InterruptionRate: -1,
		Source:          "unavailable",
	}
}
