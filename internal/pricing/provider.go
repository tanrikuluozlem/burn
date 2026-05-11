package pricing

import (
	"context"

	"github.com/tanrikuluozlem/burn/internal/collector"
)

// NodePricing holds per-resource cost rates for a node.
type NodePricing struct {
	HourlyTotal    float64 // total node hourly cost
	CPUCostPerCore float64 // cost per CPU core per hour
	RAMCostPerGiB  float64 // cost per GiB RAM per hour
	GPUCostPerUnit float64 // cost per GPU per hour (0 if no GPU)
}

// CustomPricing holds user-provided prices for on-prem nodes.
type CustomPricing struct {
	CPUCostPerCoreHr    float64
	RAMCostPerGiBHr     float64
	GPUCostPerHr        float64
	StoragePricePerGiBMo float64
}

type Provider interface {
	GetHourlyPrice(ctx context.Context, instanceType, region string, isSpot bool) (float64, error)
	GetHourlyPriceForNode(ctx context.Context, node collector.NodeInfo) (float64, error)
	GetNodePricing(ctx context.Context, node collector.NodeInfo) (*NodePricing, error)
	GetStoragePricePerGiBMonth(ctx context.Context, storageClass string) float64
	GetLoadBalancerPricePerHour() float64
	GetNetworkEgressPricePerGiB() float64
}
