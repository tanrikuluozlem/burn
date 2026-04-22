package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/ec2/imds"
	"github.com/aws/aws-sdk-go-v2/service/pricing"
	"github.com/aws/aws-sdk-go-v2/service/pricing/types"
	"gopkg.in/yaml.v3"
)

var awsRegions = []string{
	"us-east-1", "us-east-2", "us-west-1", "us-west-2",
	"eu-west-1", "eu-west-2", "eu-central-1", "eu-north-1",
	"ap-northeast-1", "ap-northeast-2", "ap-southeast-1", "ap-southeast-2",
	"ap-south-1", "sa-east-1", "ca-central-1",
}

var awsInstanceTypes = []string{
	"t3.nano", "t3.micro", "t3.small", "t3.medium", "t3.large", "t3.xlarge", "t3.2xlarge",
	"t3a.nano", "t3a.micro", "t3a.small", "t3a.medium", "t3a.large", "t3a.xlarge",
	"m5.large", "m5.xlarge", "m5.2xlarge", "m5.4xlarge",
	"m5a.large", "m5a.xlarge", "m5a.2xlarge",
	"m6i.large", "m6i.xlarge", "m6i.2xlarge",
	"c5.large", "c5.xlarge", "c5.2xlarge", "c5.4xlarge",
	"c5a.large", "c5a.xlarge", "c5a.2xlarge",
	"c6i.large", "c6i.xlarge", "c6i.2xlarge",
	"r5.large", "r5.xlarge", "r5.2xlarge",
	"r5a.large", "r5a.xlarge",
	"r6i.large", "r6i.xlarge",
}

var regionToLocation = map[string]string{
	"us-east-1":      "US East (N. Virginia)",
	"us-east-2":      "US East (Ohio)",
	"us-west-1":      "US West (N. California)",
	"us-west-2":      "US West (Oregon)",
	"eu-west-1":      "EU (Ireland)",
	"eu-west-2":      "EU (London)",
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

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run update-pricing.go [aws|azure|gcp|all]")
		os.Exit(1)
	}

	target := os.Args[1]

	switch target {
	case "aws":
		if err := updateAWS(); err != nil {
			fmt.Fprintf(os.Stderr, "AWS error: %v\n", err)
			os.Exit(1)
		}
	case "azure":
		if err := updateAzure(); err != nil {
			fmt.Fprintf(os.Stderr, "Azure error: %v\n", err)
			os.Exit(1)
		}
	case "all":
		if err := updateAWS(); err != nil {
			fmt.Fprintf(os.Stderr, "AWS error: %v\n", err)
		}
		if err := updateAzure(); err != nil {
			fmt.Fprintf(os.Stderr, "Azure error: %v\n", err)
		}
		fmt.Println("GCP: manual update required (no public API)")
	default:
		fmt.Printf("Unknown target: %s\n", target)
		os.Exit(1)
	}
}

func updateAWS() error {
	fmt.Println("Fetching AWS prices...")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Explicitly use environment credentials, skip EC2 IMDS
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion("us-east-1"),
		config.WithEC2IMDSClientEnableState(imds.ClientDisabled),
	)
	if err != nil {
		return fmt.Errorf("aws config: %w", err)
	}

	client := pricing.NewFromConfig(cfg)
	prices := make(map[string]map[string]float64)

	for _, region := range awsRegions {
		location, ok := regionToLocation[region]
		if !ok {
			continue
		}

		regionPrices := make(map[string]float64)

		for _, instanceType := range awsInstanceTypes {
			price, err := getAWSPrice(ctx, client, instanceType, location)
			if err != nil {
				fmt.Printf("  skip %s/%s: %v\n", region, instanceType, err)
				continue
			}
			regionPrices[instanceType] = price
		}

		if len(regionPrices) > 0 {
			prices[region] = regionPrices
			fmt.Printf("  %s: %d instances\n", region, len(regionPrices))
		}
	}

	return writeYAML("internal/pricing/data/aws.yaml", prices, "AWS EC2", "https://aws.amazon.com/ec2/pricing/on-demand/")
}

func getAWSPrice(ctx context.Context, client *pricing.Client, instanceType, location string) (float64, error) {
	input := &pricing.GetProductsInput{
		ServiceCode: aws.String("AmazonEC2"),
		Filters: []types.Filter{
			{Type: types.FilterTypeTermMatch, Field: aws.String("instanceType"), Value: aws.String(instanceType)},
			{Type: types.FilterTypeTermMatch, Field: aws.String("location"), Value: aws.String(location)},
			{Type: types.FilterTypeTermMatch, Field: aws.String("operatingSystem"), Value: aws.String("Linux")},
			{Type: types.FilterTypeTermMatch, Field: aws.String("tenancy"), Value: aws.String("Shared")},
			{Type: types.FilterTypeTermMatch, Field: aws.String("preInstalledSw"), Value: aws.String("NA")},
			{Type: types.FilterTypeTermMatch, Field: aws.String("capacitystatus"), Value: aws.String("Used")},
		},
		MaxResults: aws.Int32(1),
	}

	result, err := client.GetProducts(ctx, input)
	if err != nil {
		return 0, err
	}
	if len(result.PriceList) == 0 {
		return 0, fmt.Errorf("no pricing found")
	}

	return parseAWSPriceJSON(result.PriceList[0])
}

func parseAWSPriceJSON(priceJSON string) (float64, error) {
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
				var price float64
				fmt.Sscanf(dim.PricePerUnit.USD, "%f", &price)
				return price, nil
			}
		}
	}
	return 0, fmt.Errorf("no USD price")
}

func updateAzure() error {
	fmt.Println("Fetching Azure prices...")

	prices := make(map[string]map[string]float64)

	azureRegions := []string{
		"eastus", "eastus2", "westus", "westus2",
		"northeurope", "westeurope", "uksouth", "germanywestcentral",
		"southeastasia", "japaneast", "australiaeast",
	}

	vmSizes := []string{
		"Standard_B1s", "Standard_B1ms", "Standard_B2s", "Standard_B2ms", "Standard_B4ms",
		"Standard_D2s_v3", "Standard_D4s_v3", "Standard_D8s_v3",
		"Standard_D2s_v4", "Standard_D4s_v4",
		"Standard_D2s_v5", "Standard_D4s_v5", "Standard_D8s_v5",
		"Standard_D2as_v4", "Standard_D4as_v4",
		"Standard_D2as_v5", "Standard_D4as_v5",
		"Standard_E2s_v3", "Standard_E4s_v3", "Standard_E8s_v3",
		"Standard_E2s_v4", "Standard_E4s_v4",
		"Standard_E2s_v5", "Standard_E4s_v5",
		"Standard_F2s_v2", "Standard_F4s_v2", "Standard_F8s_v2",
	}

	for _, region := range azureRegions {
		regionPrices := make(map[string]float64)

		for _, vmSize := range vmSizes {
			price, err := getAzurePrice(region, vmSize)
			if err != nil {
				continue
			}
			regionPrices[vmSize] = price
		}

		if len(regionPrices) > 0 {
			prices[region] = regionPrices
			fmt.Printf("  %s: %d VM sizes\n", region, len(regionPrices))
		}
	}

	return writeYAML("internal/pricing/data/azure.yaml", prices, "Azure VM", "https://azure.microsoft.com/pricing/details/virtual-machines/linux/")
}

func getAzurePrice(region, vmSize string) (float64, error) {
	filter := fmt.Sprintf(
		"serviceName eq 'Virtual Machines' and armRegionName eq '%s' and armSkuName eq '%s' and priceType eq 'Consumption'",
		region, vmSize,
	)

	req, err := http.NewRequest("GET", "https://prices.azure.com/api/retail/prices", nil)
	if err != nil {
		return 0, err
	}

	q := req.URL.Query()
	q.Add("$filter", filter)
	req.URL.RawQuery = q.Encode()

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}

	var result struct {
		Items []struct {
			RetailPrice float64 `json:"retailPrice"`
			SkuName     string  `json:"skuName"`
			ProductName string  `json:"productName"`
		} `json:"Items"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return 0, err
	}

	for _, item := range result.Items {
		// Skip Spot and Low Priority
		if strings.Contains(item.SkuName, "Spot") || strings.Contains(item.SkuName, "Low Priority") {
			continue
		}
		// Skip Windows
		if strings.Contains(item.ProductName, "Windows") {
			continue
		}
		if item.RetailPrice > 0 {
			return item.RetailPrice, nil
		}
	}

	return 0, fmt.Errorf("no price found")
}

func writeYAML(path string, prices map[string]map[string]float64, cloudName, source string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	fmt.Fprintf(f, "# %s Instance Pricing (Linux, On-Demand)\n", cloudName)
	fmt.Fprintf(f, "# Source: %s\n", source)
	fmt.Fprintf(f, "# Auto-generated: %s\n", time.Now().UTC().Format("2006-01-02"))
	fmt.Fprintf(f, "# DO NOT EDIT MANUALLY - run 'go run scripts/update-pricing.go %s'\n\n", strings.ToLower(strings.Split(cloudName, " ")[0]))

	// Sort regions
	regions := make([]string, 0, len(prices))
	for r := range prices {
		regions = append(regions, r)
	}
	sort.Strings(regions)

	for _, region := range regions {
		instances := prices[region]

		// Sort instance types
		types := make([]string, 0, len(instances))
		for t := range instances {
			types = append(types, t)
		}
		sort.Strings(types)

		data := make(map[string]float64)
		for _, t := range types {
			data[t] = instances[t]
		}

		regionData := map[string]map[string]float64{region: data}
		out, err := yaml.Marshal(regionData)
		if err != nil {
			return err
		}
		f.Write(out)
		f.WriteString("\n")
	}

	fmt.Printf("Written: %s\n", path)
	return nil
}
