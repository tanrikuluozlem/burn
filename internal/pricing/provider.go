package pricing

import (
	"context"

	"github.com/tanrikuluozlem/burn/internal/collector"
)

type NodePricing struct {
	HourlyTotal    float64
	CPUCostPerCore float64
	RAMCostPerGiB  float64
	GPUCostPerUnit float64
}

type CustomPricing struct {
	CPUCostPerCoreHr     float64
	RAMCostPerGiBHr      float64
	GPUCostPerHr         float64
	StoragePricePerGiBMo float64
}

type SpotDiscount struct {
	Discount         float64 // 0.0-1.0
	InterruptionRate int     // 0-4 scale, -1 = unknown
	Source           string  // "api", "advisor", "default"
}

type Provider interface {
	GetHourlyPrice(ctx context.Context, instanceType, region string, isSpot bool) (float64, error)
	GetHourlyPriceForNode(ctx context.Context, node collector.NodeInfo) (float64, error)
	GetNodePricing(ctx context.Context, node collector.NodeInfo) (*NodePricing, error)
	GetStoragePricePerGiBMonth(ctx context.Context, storageClass string) float64
	GetLoadBalancerPricePerHour() float64
	GetNetworkEgressPricePerGiB() float64
	GetSpotDiscount(ctx context.Context, instanceType, region string) SpotDiscount
}
