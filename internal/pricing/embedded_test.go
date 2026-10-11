package pricing

import (
	"testing"
)

func TestGetEmbeddedPrice_AWS(t *testing.T) {
	tests := []struct {
		region       string
		instanceType string
		wantPrice    float64
	}{
		{"us-east-1", "t3.micro", 0.0104},
		{"us-east-1", "m5.large", 0.096},
		{"eu-central-1", "t3.large", 0.096},
		{"ap-northeast-1", "c5.large", 0.107},
	}

	for _, tt := range tests {
		price, err := GetEmbeddedPrice("aws", tt.region, tt.instanceType)
		if err != nil {
			t.Errorf("GetEmbeddedPrice(aws, %s, %s) error = %v", tt.region, tt.instanceType, err)
			continue
		}
		if price != tt.wantPrice {
			t.Errorf("GetEmbeddedPrice(aws, %s, %s) = %v, want %v", tt.region, tt.instanceType, price, tt.wantPrice)
		}
	}
}

func TestGetEmbeddedPrice_Azure(t *testing.T) {
	tests := []struct {
		region   string
		vmSize   string
		wantPrice float64
	}{
		{"eastus", "Standard_D2s_v3", 0.096},
		{"westeurope", "Standard_B2s", 0.048},
	}

	for _, tt := range tests {
		price, err := GetEmbeddedPrice("azure", tt.region, tt.vmSize)
		if err != nil {
			t.Errorf("GetEmbeddedPrice(azure, %s, %s) error = %v", tt.region, tt.vmSize, err)
			continue
		}
		if price != tt.wantPrice {
			t.Errorf("GetEmbeddedPrice(azure, %s, %s) = %v, want %v", tt.region, tt.vmSize, price, tt.wantPrice)
		}
	}
}

func TestGetEmbeddedPrice_GCP(t *testing.T) {
	tests := []struct {
		region      string
		machineType string
		wantPrice   float64
	}{
		{"us-central1", "e2-medium", 0.0336},
		{"europe-west1", "n1-standard-2", 0.1045},
	}

	for _, tt := range tests {
		price, err := GetEmbeddedPrice("gcp", tt.region, tt.machineType)
		if err != nil {
			t.Errorf("GetEmbeddedPrice(gcp, %s, %s) error = %v", tt.region, tt.machineType, err)
			continue
		}
		if price != tt.wantPrice {
			t.Errorf("GetEmbeddedPrice(gcp, %s, %s) = %v, want %v", tt.region, tt.machineType, price, tt.wantPrice)
		}
	}
}

func TestGetEmbeddedPrice_UnknownRegionFallback(t *testing.T) {
	// Unknown region should fallback to default region
	price, err := GetEmbeddedPrice("aws", "unknown-region", "t3.micro")
	if err != nil {
		t.Errorf("expected fallback to us-east-1, got error: %v", err)
	}
	if price != 0.0104 {
		t.Errorf("expected us-east-1 fallback price 0.0104, got %v", price)
	}
}

func TestGetEmbeddedPrice_NotFound(t *testing.T) {
	_, err := GetEmbeddedPrice("aws", "us-east-1", "unknown-instance")
	if err == nil {
		t.Error("expected error for unknown instance type")
	}
}

func TestGetEmbeddedPrice_UnknownCloud(t *testing.T) {
	_, err := GetEmbeddedPrice("unknown", "us-east-1", "t3.micro")
	if err == nil {
		t.Error("expected error for unknown cloud")
	}
}

func TestGetEmbeddedPrice_VerifiedEntries(t *testing.T) {
	tests := []struct {
		cloud        string
		region       string
		instanceType string
		wantPrice    float64
	}{
		{"aws", "eu-west-2", "r5.large", 0.148},
		{"aws", "sa-east-1", "r5.2xlarge", 0.804},
		{"azure", "eastus2", "Standard_E2s_v4", 0.126},
		{"azure", "northeurope", "Standard_D4as_v4", 0.214},
		{"azure", "northeurope", "Standard_D4s_v3", 0.214},
		{"azure", "northeurope", "Standard_E2s_v3", 0.141},
		{"azure", "uksouth", "Standard_D4s_v5", 0.222},
		{"azure", "westeurope", "Standard_D2as_v5", 0.104},
		{"azure", "westus", "Standard_B2ms", 0.0992},
		{"azure", "australiaeast", "Standard_D2as_v5", 0.108},
		{"azure", "eastus", "Standard_D4as_v5", 0.172},
		{"azure", "germanywestcentral", "Standard_D2as_v5", 0.104},
		{"azure", "northeurope", "Standard_D4as_v5", 0.192},
		{"azure", "southeastasia", "Standard_D2as_v5", 0.108},
		{"azure", "southeastasia", "Standard_D4as_v5", 0.216},
		{"azure", "uksouth", "Standard_D2as_v5", 0.1},
		{"azure", "westeurope", "Standard_D4as_v5", 0.208},
		{"azure", "westus", "Standard_D2as_v5", 0.101},
		{"azure", "westus2", "Standard_D4as_v5", 0.172},
	}

	for _, tt := range tests {
		price, err := GetEmbeddedPrice(tt.cloud, tt.region, tt.instanceType)
		if err != nil {
			t.Errorf("GetEmbeddedPrice(%s, %s, %s) error = %v", tt.cloud, tt.region, tt.instanceType, err)
			continue
		}
		if price != tt.wantPrice {
			t.Errorf("GetEmbeddedPrice(%s, %s, %s) = %v, want %v",
				tt.cloud, tt.region, tt.instanceType, price, tt.wantPrice)
		}
	}
}

func TestGetEmbeddedPrice_WestEuropeD2asV5NotCloudServices(t *testing.T) {
	price, err := GetEmbeddedPrice("azure", "westeurope", "Standard_D2as_v5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if price != 0.104 {
		t.Errorf("westeurope/Standard_D2as_v5 = %v, want 0.104", price)
	}
}
