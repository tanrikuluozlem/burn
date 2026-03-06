package pricing

import "context"

type Provider interface {
	GetHourlyPrice(ctx context.Context, instanceType, region string, isSpot bool) (float64, error)
}
