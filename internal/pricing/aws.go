package pricing

import "fmt"

// AWSProvider fetches real-time pricing from AWS Pricing API
// TODO: implement when AWS credentials available
type AWSProvider struct {
	fallback *StaticProvider
}

func NewAWSProvider() *AWSProvider {
	return &AWSProvider{
		fallback: NewStaticProvider(),
	}
}

func (p *AWSProvider) GetHourlyPrice(instanceType, region string, isSpot bool) (float64, error) {
	// TODO: call AWS Pricing API
	// For now, use static fallback
	price, err := p.fallback.GetHourlyPrice(instanceType, region, isSpot)
	if err != nil {
		return 0, fmt.Errorf("pricing lookup failed (AWS API not configured, fallback also failed): %w", err)
	}
	return price, nil
}
