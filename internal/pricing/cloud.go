package pricing

import "fmt"

type CloudProvider struct {
	fallback *StaticProvider
}

func NewCloudProvider() *CloudProvider {
	return &CloudProvider{
		fallback: NewStaticProvider(),
	}
}

func (p *CloudProvider) GetHourlyPrice(instanceType, region string, isSpot bool) (float64, error) {
	price, err := p.fallback.GetHourlyPrice(instanceType, region, isSpot)
	if err != nil {
		return 0, fmt.Errorf("pricing not available: %w", err)
	}
	return price, nil
}
