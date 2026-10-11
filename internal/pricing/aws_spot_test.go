package pricing

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/tanrikuluozlem/burn/internal/collector"
)

type fakeSpotAPI struct {
	calls  []*ec2.DescribeSpotPriceHistoryInput
	prices map[string]string
}

func (f *fakeSpotAPI) DescribeSpotPriceHistory(_ context.Context, params *ec2.DescribeSpotPriceHistoryInput, _ ...func(*ec2.Options)) (*ec2.DescribeSpotPriceHistoryOutput, error) {
	f.calls = append(f.calls, params)

	zone := ""
	if params.AvailabilityZone != nil {
		zone = *params.AvailabilityZone
	}
	price, ok := f.prices[zone]
	if !ok {
		return &ec2.DescribeSpotPriceHistoryOutput{}, nil
	}
	return &ec2.DescribeSpotPriceHistoryOutput{
		SpotPriceHistory: []ec2types.SpotPrice{{
			AvailabilityZone: aws.String(zone),
			SpotPrice:        aws.String(price),
		}},
	}, nil
}

func awsProviderWithSpotAPI(f *fakeSpotAPI) *AWSProvider {
	return &AWSProvider{
		ec2Clients: map[string]spotPriceHistoryAPI{"eu-central-1": f},
		cache:      make(map[string]cachedPrice),
	}
}

func cloudProviderWithAWS(p *AWSProvider) *CloudPricingProvider {
	cp := &CloudPricingProvider{azure: NewAzureProvider(), fallback: NewStaticProvider()}
	cp.awsOnce.Do(func() {})
	cp.aws = p
	return cp
}

func TestGetHourlyPrice_SpotUsesNodeZone(t *testing.T) {
	f := &fakeSpotAPI{prices: map[string]string{
		"":              "0.044600",
		"eu-central-1a": "0.046600",
		"eu-central-1b": "0.044600",
	}}
	cp := cloudProviderWithAWS(awsProviderWithSpotAPI(f))

	price, err := cp.GetHourlyPriceForNode(context.Background(), collector.NodeInfo{
		InstanceType:  "t3.large",
		Region:        "eu-central-1",
		Zone:          "eu-central-1a",
		IsSpot:        true,
		CloudProvider: collector.CloudAWS,
	})
	if err != nil {
		t.Fatalf("GetHourlyPriceForNode() error = %v", err)
	}
	if price != 0.0466 {
		t.Errorf("price = %v, want 0.0466", price)
	}
	if len(f.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(f.calls))
	}
	az := f.calls[0].AvailabilityZone
	if az == nil || *az != "eu-central-1a" {
		t.Errorf("AvailabilityZone = %v, want eu-central-1a", az)
	}
}

func TestGetHourlyPrice_SpotZonesDoNotShareCache(t *testing.T) {
	f := &fakeSpotAPI{prices: map[string]string{
		"eu-central-1a": "0.046600",
		"eu-central-1b": "0.044600",
	}}
	p := awsProviderWithSpotAPI(f)
	ctx := context.Background()

	a, err := p.getHourlyPrice(ctx, "t3.large", "eu-central-1", "eu-central-1a", true)
	if err != nil {
		t.Fatalf("eu-central-1a error = %v", err)
	}
	b, err := p.getHourlyPrice(ctx, "t3.large", "eu-central-1", "eu-central-1b", true)
	if err != nil {
		t.Fatalf("eu-central-1b error = %v", err)
	}

	if a != 0.0466 {
		t.Errorf("eu-central-1a = %v, want 0.0466", a)
	}
	if b != 0.0446 {
		t.Errorf("eu-central-1b = %v, want 0.0446", b)
	}
	if len(f.calls) != 2 {
		t.Errorf("calls = %d, want 2", len(f.calls))
	}
}

func TestGetHourlyPrice_SpotSameZoneUsesCache(t *testing.T) {
	f := &fakeSpotAPI{prices: map[string]string{"eu-central-1a": "0.046600"}}
	p := awsProviderWithSpotAPI(f)
	ctx := context.Background()

	first, err := p.getHourlyPrice(ctx, "t3.large", "eu-central-1", "eu-central-1a", true)
	if err != nil {
		t.Fatalf("first call error = %v", err)
	}
	second, err := p.getHourlyPrice(ctx, "t3.large", "eu-central-1", "eu-central-1a", true)
	if err != nil {
		t.Fatalf("second call error = %v", err)
	}

	if first != second {
		t.Errorf("first = %v, second = %v", first, second)
	}
	if len(f.calls) != 1 {
		t.Errorf("calls = %d, want 1", len(f.calls))
	}
}

func TestGetHourlyPrice_SpotEmptyZoneUnfiltered(t *testing.T) {
	f := &fakeSpotAPI{prices: map[string]string{"": "0.044600"}}
	p := awsProviderWithSpotAPI(f)

	price, err := p.getHourlyPrice(context.Background(), "t3.large", "eu-central-1", "", true)
	if err != nil {
		t.Fatalf("GetHourlyPrice() error = %v", err)
	}
	if price != 0.0446 {
		t.Errorf("price = %v, want 0.0446", price)
	}
	if len(f.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(f.calls))
	}

	in := f.calls[0]
	if in.AvailabilityZone != nil {
		t.Errorf("AvailabilityZone = %v, want nil", *in.AvailabilityZone)
	}
	if in.Filters != nil {
		t.Errorf("Filters = %v, want nil", in.Filters)
	}
	if in.MaxResults == nil || *in.MaxResults != 1 {
		t.Errorf("MaxResults = %v, want 1", in.MaxResults)
	}
	if len(in.InstanceTypes) != 1 || in.InstanceTypes[0] != ec2types.InstanceType("t3.large") {
		t.Errorf("InstanceTypes = %v, want [t3.large]", in.InstanceTypes)
	}
	if len(in.ProductDescriptions) != 1 || in.ProductDescriptions[0] != "Linux/UNIX" {
		t.Errorf("ProductDescriptions = %v, want [Linux/UNIX]", in.ProductDescriptions)
	}
	if in.StartTime == nil {
		t.Error("StartTime = nil, want non-nil")
	}
}

func TestPriceCacheKey_OnDemandIgnoresZone(t *testing.T) {
	odA := priceCacheKey("t3.large", "eu-central-1", "eu-central-1a", false)
	odB := priceCacheKey("t3.large", "eu-central-1", "eu-central-1b", false)
	odNone := priceCacheKey("t3.large", "eu-central-1", "", false)

	if odA != odB || odA != odNone {
		t.Errorf("on-demand keys differ: %q, %q, %q", odA, odB, odNone)
	}

	spotA := priceCacheKey("t3.large", "eu-central-1", "eu-central-1a", true)
	spotB := priceCacheKey("t3.large", "eu-central-1", "eu-central-1b", true)

	if spotA == spotB {
		t.Errorf("spot keys must differ per zone, both %q", spotA)
	}
	if spotA == odA {
		t.Errorf("spot and on-demand keys must differ, both %q", spotA)
	}
}
