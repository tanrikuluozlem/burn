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
}

type Provider interface {
	GetHourlyPrice(ctx context.Context, instanceType, region string, isSpot bool) (float64, error)
	GetHourlyPriceForNode(ctx context.Context, node collector.NodeInfo) (float64, error)
	GetNodePricing(ctx context.Context, node collector.NodeInfo) (*NodePricing, error)
}
