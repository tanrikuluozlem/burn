package pricing

import "fmt"

// AWSProvider wraps AWS Pricing API with static fallback
type AWSProvider struct {
	fallback *StaticProvider
}

func NewAWSProvider() *AWSProvider {
	return &AWSProvider{
		fallback: NewStaticProvider(),
	}
}

func (p *AWSProvider) GetHourlyPrice(instanceType, region string, isSpot bool) (float64, error) {
	// uses static fallback until AWS credentials configured
	price, err := p.fallback.GetHourlyPrice(instanceType, region, isSpot)
	if err != nil {
		return 0, fmt.Errorf("pricing lookup failed (AWS API not configured, fallback also failed): %w", err)
	}
	return price, nil
}
