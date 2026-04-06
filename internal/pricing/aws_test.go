package pricing

import "testing"

func TestAwsRegionToCode(t *testing.T) {
	tests := []struct {
		region string
		want   string
	}{
		{"us-east-1", "US East (N. Virginia)"},
		{"eu-central-1", "EU (Frankfurt)"},
		{"ap-northeast-1", "Asia Pacific (Tokyo)"},
		{"unknown-region", "unknown-region"},
	}

	for _, tc := range tests {
		got := awsRegionToCode(tc.region)
		if got != tc.want {
			t.Errorf("awsRegionToCode(%s) = %s, want %s", tc.region, got, tc.want)
		}
	}
}

func TestParseOnDemandPrice(t *testing.T) {
	validJSON := `{
		"terms": {
			"OnDemand": {
				"OFFER123": {
					"priceDimensions": {
						"DIM456": {
							"pricePerUnit": {"USD": "0.0416"}
						}
					}
				}
			}
		}
	}`

	price, err := parseOnDemandPrice(validJSON)
	if err != nil {
		t.Fatal(err)
	}
	if price != 0.0416 {
		t.Errorf("price = %v, want 0.0416", price)
	}
}

func TestParseOnDemandPriceInvalid(t *testing.T) {
	_, err := parseOnDemandPrice("not json")
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestParseOnDemandPriceEmpty(t *testing.T) {
	emptyJSON := `{"terms": {"OnDemand": {}}}`
	_, err := parseOnDemandPrice(emptyJSON)
	if err == nil {
		t.Error("expected error for empty price data")
	}
}
