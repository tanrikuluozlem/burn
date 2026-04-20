package pricing

import (
	"context"
	"testing"
)

func TestStaticProvider(t *testing.T) {
	p := NewStaticProvider()
	ctx := context.Background()

	price, err := p.GetHourlyPrice(ctx, "t3.medium", "us-east-1", false)
	if err != nil {
		t.Fatal(err)
	}
	if price != 0.0416 {
		t.Errorf("t3.medium price = %v, want 0.0416", price)
	}
}

func TestStaticProviderSpot(t *testing.T) {
	p := NewStaticProvider()

	price, _ := p.GetHourlyPrice(context.Background(), "t3.medium", "us-east-1", true)
	// Spot price is 35% of on-demand (65% discount)
	expected := 0.0416 * 0.35
	// Use tolerance for floating point comparison
	diff := price - expected
	if diff < 0 {
		diff = -diff
	}
	if diff > 0.0001 {
		t.Errorf("spot price = %v, want ~%v", price, expected)
	}
}

func TestStaticProviderUnknownRegion(t *testing.T) {
	p := NewStaticProvider()

	price, err := p.GetHourlyPrice(context.Background(), "t3.medium", "ap-south-1", false)
	if err != nil {
		t.Fatal(err)
	}
	if price != 0.0416 {
		t.Error("should fallback to us-east-1 prices")
	}
}

func TestStaticProviderUnknownInstance(t *testing.T) {
	p := NewStaticProvider()
	ctx := context.Background()

	// Unknown instance types should get estimated prices based on size suffix
	tests := []struct {
		instanceType string
		wantPrice    float64
	}{
		{"x99.large", 0.10},    // matches "large" suffix
		{"x99.xlarge", 0.20},   // matches "xlarge" suffix
		{"x99.2xlarge", 0.40},  // matches "2xlarge" suffix
		{"x99.medium", 0.04},   // matches "medium" suffix
		{"unknown", 0.10},      // no match, uses default
	}

	for _, tt := range tests {
		price, err := p.GetHourlyPrice(ctx, tt.instanceType, "us-east-1", false)
		if err != nil {
			t.Errorf("GetHourlyPrice(%s) error = %v, want nil", tt.instanceType, err)
		}
		if price != tt.wantPrice {
			t.Errorf("GetHourlyPrice(%s) = %v, want %v", tt.instanceType, price, tt.wantPrice)
		}
	}
}
