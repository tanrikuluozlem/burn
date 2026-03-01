package pricing

type Provider interface {
	GetHourlyPrice(instanceType, region string, isSpot bool) (float64, error)
}
