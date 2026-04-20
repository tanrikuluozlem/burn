package pricing

import (
	"context"

	"github.com/tanrikuluozlem/burn/internal/collector"
)

type Provider interface {
	GetHourlyPrice(ctx context.Context, instanceType, region string, isSpot bool) (float64, error)
	GetHourlyPriceForNode(ctx context.Context, node collector.NodeInfo) (float64, error)
}
