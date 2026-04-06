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
	expected := 0.0416 * 0.3
	if price != expected {
		t.Errorf("spot price = %v, want %v", price, expected)
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

	_, err := p.GetHourlyPrice(context.Background(), "x99.mega", "us-east-1", false)
	if err == nil {
		t.Error("expected error for unknown instance type")
	}
}
