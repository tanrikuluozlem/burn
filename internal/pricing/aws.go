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

type AWSProvider struct {
	pricingClient *pricing.Client
	ec2Clients    map[string]*ec2.Client
	cache         map[string]cachedPrice
	mu            sync.RWMutex
}

type cachedPrice struct {
	price     float64
	expiresAt time.Time
}

func NewAWSProvider(ctx context.Context) (*AWSProvider, error) {
	// Use a short timeout for credential loading to fail fast when AWS isn't configured
	loadCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	cfg, err := config.LoadDefaultConfig(loadCtx, config.WithRegion("us-east-1"))
	if err != nil {
		return nil, fmt.Errorf("aws config: %w", err)
	}

	// Quick credential check - fail fast if no credentials available
	creds, err := cfg.Credentials.Retrieve(loadCtx)
	if err != nil {
		return nil, fmt.Errorf("aws credentials: %w", err)
	}
	if creds.AccessKeyID == "" {
		return nil, fmt.Errorf("aws credentials not configured")
	}

	return &AWSProvider{
		pricingClient: pricing.NewFromConfig(cfg),
		ec2Clients:    make(map[string]*ec2.Client),
		cache:         make(map[string]cachedPrice),
	}, nil
}

func (p *AWSProvider) GetHourlyPrice(ctx context.Context, instanceType, region string, isSpot bool) (float64, error) {
	key := fmt.Sprintf("%s:%s:%v", instanceType, region, isSpot)

	p.mu.RLock()
	if c, ok := p.cache[key]; ok && time.Now().Before(c.expiresAt) {
		p.mu.RUnlock()
		return c.price, nil
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

	p.mu.Lock()
	if c, ok := p.cache[key]; ok && time.Now().Before(c.expiresAt) {
		p.mu.Unlock()
		return c.price, nil
	}
	p.cache[key] = cachedPrice{price: price, expiresAt: time.Now().Add(time.Hour)}
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
		return 0, err
	}
	if len(result.PriceList) == 0 {
		return 0, fmt.Errorf("no pricing for %s in %s", instanceType, region)
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
		return 0, err
	}
	if len(result.SpotPriceHistory) == 0 {
		return 0, fmt.Errorf("no spot pricing for %s in %s", instanceType, region)
	}
	return strconv.ParseFloat(*result.SpotPriceHistory[0].SpotPrice, 64)
}

func (p *AWSProvider) getEC2Client(ctx context.Context, region string) (*ec2.Client, error) {
	p.mu.RLock()
	if c, ok := p.ec2Clients[region]; ok {
		p.mu.RUnlock()
		return c, nil
	}
	p.mu.RUnlock()

	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, err
	}

	c := ec2.NewFromConfig(cfg)
	p.mu.Lock()
	if existing, ok := p.ec2Clients[region]; ok {
		p.mu.Unlock()
		return existing, nil
	}
	p.ec2Clients[region] = c
	p.mu.Unlock()
	return c, nil
}

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
		return 0, err
	}
	for _, offer := range data.Terms.OnDemand {
		for _, dim := range offer.PriceDimensions {
			if dim.PricePerUnit.USD != "" {
				return strconv.ParseFloat(dim.PricePerUnit.USD, 64)
			}
		}
	}
	return 0, fmt.Errorf("no USD price in response")
}

func (p *AWSProvider) GetEBSPrice(ctx context.Context, volumeType, region string) (float64, error) {
	key := fmt.Sprintf("ebs:%s:%s", volumeType, region)

	p.mu.RLock()
	if c, ok := p.cache[key]; ok && time.Now().Before(c.expiresAt) {
		p.mu.RUnlock()
		return c.price, nil
	}
	p.mu.RUnlock()

	usageType := ebsUsageType(volumeType, region)
	regionCode := awsRegionToCode(region)

	input := &pricing.GetProductsInput{
		ServiceCode: aws.String("AmazonEC2"),
		Filters: []types.Filter{
			{
				Type:  types.FilterTypeTermMatch,
				Field: aws.String("productFamily"),
				Value: aws.String("Storage"),
			},
			{
				Type:  types.FilterTypeTermMatch,
				Field: aws.String("location"),
				Value: aws.String(regionCode),
			},
			{
				Type:  types.FilterTypeTermMatch,
				Field: aws.String("usagetype"),
				Value: aws.String(usageType),
			},
		},
		MaxResults: aws.Int32(1),
	}

	result, err := p.pricingClient.GetProducts(ctx, input)
	if err != nil {
		return 0, err
	}
	if len(result.PriceList) == 0 {
		return 0, fmt.Errorf("no EBS pricing for %s in %s", volumeType, region)
	}

	price, err := parseOnDemandPrice(result.PriceList[0])
	if err != nil {
		return 0, err
	}

	p.mu.Lock()
	p.cache[key] = cachedPrice{price: price, expiresAt: time.Now().Add(time.Hour)}
	p.mu.Unlock()

	return price, nil
}

func ebsUsageType(volumeType, region string) string {
	prefix := ""
	if region != "us-east-1" {
		parts := map[string]string{
			"eu-central-1": "EUC1", "eu-west-1": "EU", "eu-west-2": "EUW2", "eu-west-3": "EUW3",
			"eu-north-1": "EUN1", "us-east-2": "USE2", "us-west-1": "USW1", "us-west-2": "USW2",
			"ap-northeast-1": "APN1", "ap-northeast-2": "APN2", "ap-southeast-1": "APS1",
			"ap-southeast-2": "APS2", "ap-south-1": "APS3", "sa-east-1": "SAE1", "ca-central-1": "CAN1",
		}
		if p, ok := parts[region]; ok {
			prefix = p + "-"
		}
	}

	volMap := map[string]string{
		"gp2": "EBS:VolumeUsage.gp2", "gp3": "EBS:VolumeUsage.gp3",
		"io1": "EBS:VolumeUsage.piops", "io2": "EBS:VolumeUsage.gp3.piops",
		"st1": "EBS:VolumeUsage.st1", "sc1": "EBS:VolumeUsage.sc1",
	}

	usage := "EBS:VolumeUsage.gp2"
	if u, ok := volMap[volumeType]; ok {
		usage = u
	}
	return prefix + usage
}

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
