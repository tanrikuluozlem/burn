package pricing

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/pricing"
	"github.com/aws/aws-sdk-go-v2/service/pricing/types"
)

// AWSProvider fetches real-time pricing from AWS APIs
type AWSProvider struct {
	pricingClient *pricing.Client        // for on-demand prices (us-east-1 only)
	ec2Clients    map[string]*ec2.Client // per-region clients for spot prices
	cache         map[string]cachedPrice
	mu            sync.RWMutex
}

type cachedPrice struct {
	price     float64
	expiresAt time.Time
}

func NewAWSProvider(ctx context.Context) (*AWSProvider, error) {
	// pricing API only available in us-east-1
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion("us-east-1"))
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	return &AWSProvider{
		pricingClient: pricing.NewFromConfig(cfg),
		ec2Clients:    make(map[string]*ec2.Client),
		cache:         make(map[string]cachedPrice),
	}, nil
}

func (p *AWSProvider) GetHourlyPrice(ctx context.Context, instanceType, region string, isSpot bool) (float64, error) {
	cacheKey := fmt.Sprintf("%s:%s:%v", instanceType, region, isSpot)

	// check cache first
	p.mu.RLock()
	if cached, ok := p.cache[cacheKey]; ok && time.Now().Before(cached.expiresAt) {
		p.mu.RUnlock()
		return cached.price, nil
	}
	p.mu.RUnlock()

	var price float64
	var err error

	if isSpot {
		price, err = p.getSpotPrice(ctx, instanceType, region)
	} else {
		price, err = p.getOnDemandPrice(ctx, instanceType, region)
	}

	if err != nil {
		return 0, err
	}

	// cache for 1 hour
	p.mu.Lock()
	p.cache[cacheKey] = cachedPrice{
		price:     price,
		expiresAt: time.Now().Add(1 * time.Hour),
	}
	p.mu.Unlock()

	return price, nil
}

func (p *AWSProvider) getOnDemandPrice(ctx context.Context, instanceType, region string) (float64, error) {
	regionCode := awsRegionToCode(region)

	input := &pricing.GetProductsInput{
		ServiceCode: aws.String("AmazonEC2"),
		Filters: []types.Filter{
			{
				Type:  types.FilterTypeTermMatch,
				Field: aws.String("instanceType"),
				Value: aws.String(instanceType),
			},
			{
				Type:  types.FilterTypeTermMatch,
				Field: aws.String("location"),
				Value: aws.String(regionCode),
			},
			{
				Type:  types.FilterTypeTermMatch,
				Field: aws.String("operatingSystem"),
				Value: aws.String("Linux"),
			},
			{
				Type:  types.FilterTypeTermMatch,
				Field: aws.String("tenancy"),
				Value: aws.String("Shared"),
			},
			{
				Type:  types.FilterTypeTermMatch,
				Field: aws.String("preInstalledSw"),
				Value: aws.String("NA"),
			},
			{
				Type:  types.FilterTypeTermMatch,
				Field: aws.String("capacitystatus"),
				Value: aws.String("Used"),
			},
		},
		MaxResults: aws.Int32(1),
	}

	result, err := p.pricingClient.GetProducts(ctx, input)
	if err != nil {
		return 0, fmt.Errorf("pricing API error: %w", err)
	}

	if len(result.PriceList) == 0 {
		return 0, fmt.Errorf("no pricing found for %s in %s", instanceType, region)
	}

	return parseOnDemandPrice(result.PriceList[0])
}

func (p *AWSProvider) getSpotPrice(ctx context.Context, instanceType, region string) (float64, error) {
	client, err := p.getEC2Client(ctx, region)
	if err != nil {
		return 0, err
	}

	input := &ec2.DescribeSpotPriceHistoryInput{
		InstanceTypes: []ec2types.InstanceType{ec2types.InstanceType(instanceType)},
		ProductDescriptions: []string{
			"Linux/UNIX",
		},
		StartTime:  aws.Time(time.Now().Add(-1 * time.Hour)),
		MaxResults: aws.Int32(1),
	}

	result, err := client.DescribeSpotPriceHistory(ctx, input)
	if err != nil {
		return 0, fmt.Errorf("spot price API error: %w", err)
	}

	if len(result.SpotPriceHistory) == 0 {
		return 0, fmt.Errorf("no spot pricing found for %s in %s", instanceType, region)
	}

	price, err := strconv.ParseFloat(*result.SpotPriceHistory[0].SpotPrice, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid spot price: %w", err)
	}

	return price, nil
}

func (p *AWSProvider) getEC2Client(ctx context.Context, region string) (*ec2.Client, error) {
	p.mu.RLock()
	if client, ok := p.ec2Clients[region]; ok {
		p.mu.RUnlock()
		return client, nil
	}
	p.mu.RUnlock()

	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("failed to load config for region %s: %w", region, err)
	}

	client := ec2.NewFromConfig(cfg)

	p.mu.Lock()
	p.ec2Clients[region] = client
	p.mu.Unlock()

	return client, nil
}

// parseOnDemandPrice extracts hourly USD price from AWS pricing API response
func parseOnDemandPrice(priceJSON string) (float64, error) {
	var data struct {
		Terms struct {
			OnDemand map[string]struct {
				PriceDimensions map[string]struct {
					PricePerUnit struct {
						USD string `json:"USD"`
					} `json:"pricePerUnit"`
				} `json:"priceDimensions"`
			} `json:"OnDemand"`
		} `json:"terms"`
	}

	if err := json.Unmarshal([]byte(priceJSON), &data); err != nil {
		return 0, fmt.Errorf("failed to parse price JSON: %w", err)
	}

	for _, offer := range data.Terms.OnDemand {
		for _, dim := range offer.PriceDimensions {
			if dim.PricePerUnit.USD != "" {
				price, err := strconv.ParseFloat(dim.PricePerUnit.USD, 64)
				if err != nil {
					return 0, fmt.Errorf("invalid price value: %w", err)
				}
				return price, nil
			}
		}
	}

	return 0, fmt.Errorf("no USD price found in response")
}

// awsRegionToCode converts AWS region ID to location name used by Pricing API
func awsRegionToCode(region string) string {
	regionMap := map[string]string{
		"us-east-1":      "US East (N. Virginia)",
		"us-east-2":      "US East (Ohio)",
		"us-west-1":      "US West (N. California)",
		"us-west-2":      "US West (Oregon)",
		"eu-west-1":      "EU (Ireland)",
		"eu-west-2":      "EU (London)",
		"eu-west-3":      "EU (Paris)",
		"eu-central-1":   "EU (Frankfurt)",
		"eu-north-1":     "EU (Stockholm)",
		"ap-northeast-1": "Asia Pacific (Tokyo)",
		"ap-northeast-2": "Asia Pacific (Seoul)",
		"ap-southeast-1": "Asia Pacific (Singapore)",
		"ap-southeast-2": "Asia Pacific (Sydney)",
		"ap-south-1":     "Asia Pacific (Mumbai)",
		"sa-east-1":      "South America (Sao Paulo)",
		"ca-central-1":   "Canada (Central)",
	}

	if code, ok := regionMap[region]; ok {
		return code
	}
	return region
}
