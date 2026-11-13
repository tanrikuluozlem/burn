package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func writeCatalog(t *testing.T, dir, name string, prices map[string]map[string]float64) (string, []byte) {
	t.Helper()
	path := filepath.Join(dir, name)
	data, err := yaml.Marshal(prices)
	if err != nil {
		t.Fatalf("marshal catalog: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write catalog: %v", err)
	}
	return path, data
}

func assertUnchanged(t *testing.T, path string, original []byte) {
	t.Helper()
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(current) != string(original) {
		t.Errorf("%s was rewritten, expected it to stay untouched", filepath.Base(path))
	}
}

func fetchFrom(prices map[string]float64, failures map[string]error) func(string, []string) (map[string]float64, error) {
	return func(region string, skus []string) (map[string]float64, error) {
		found := make(map[string]float64, len(skus))
		for _, sku := range skus {
			key := region + "/" + sku
			if err, ok := failures[key]; ok {
				return nil, err
			}
			if price, ok := prices[key]; ok {
				found[sku] = price
			}
		}
		return found, nil
	}
}

func TestBuildValidatedCatalogExistingKeyErrorBlocks(t *testing.T) {
	previous := map[string]map[string]float64{
		"eu-west-2": {"r5.large": 0.148, "r5.xlarge": 0.296},
	}
	path, original := writeCatalog(t, t.TempDir(), "aws.yaml", previous)

	_, err := buildValidatedCatalog(catalogSpec{
		path:    path,
		regions: []string{"eu-west-2"},
		skus:    []string{"r5.large", "r5.xlarge"},
		fetch: fetchFrom(
			map[string]float64{"eu-west-2/r5.xlarge": 0.296},
			map[string]error{"eu-west-2/r5.large": errors.New("throttled")},
		),
	})

	if err == nil {
		t.Fatal("expected a known key failure to block the refresh")
	}
	if !strings.Contains(err.Error(), "eu-west-2/r5.large") {
		t.Errorf("error should name the failed key, got %v", err)
	}
	assertUnchanged(t, path, original)
}

func TestBuildValidatedCatalogAbsentKeyErrorDoesNotBlock(t *testing.T) {
	previous := map[string]map[string]float64{"eu-north-1": {"t3.large": 0.0864}}
	path, _ := writeCatalog(t, t.TempDir(), "aws.yaml", previous)

	prices, err := buildValidatedCatalog(catalogSpec{
		path:    path,
		regions: []string{"eu-north-1"},
		skus:    []string{"t3.large", "t3a.large", "r5a.large"},
		fetch: fetchFrom(
			map[string]float64{"eu-north-1/t3.large": 0.0864},
			map[string]error{
				"eu-north-1/t3a.large": errors.New("no pricing found"),
				"eu-north-1/r5a.large": errors.New("no pricing found"),
			},
		),
	})

	if err != nil {
		t.Fatalf("absent key failures must not block: %v", err)
	}
	if prices["eu-north-1"]["t3.large"] != 0.0864 {
		t.Errorf("t3.large = %v, want 0.0864", prices["eu-north-1"]["t3.large"])
	}
	for _, sku := range []string{"t3a.large", "r5a.large"} {
		if _, ok := prices["eu-north-1"][sku]; ok {
			t.Errorf("invented a value for %s", sku)
		}
	}
}

func TestBuildValidatedCatalogLostKeyFails(t *testing.T) {
	previous := map[string]map[string]float64{
		"westeurope": {"Standard_D2as_v5": 0.104, "Standard_B2s": 0.048},
	}
	path, original := writeCatalog(t, t.TempDir(), "azure.yaml", previous)

	_, err := buildValidatedCatalog(catalogSpec{
		path:    path,
		regions: []string{"westeurope"},
		skus:    []string{"Standard_B2s"},
		fetch:   fetchFrom(map[string]float64{"westeurope/Standard_B2s": 0.048}, nil),
	})

	if err == nil {
		t.Fatal("expected a lost key to fail the refresh")
	}
	if !strings.Contains(err.Error(), "westeurope/Standard_D2as_v5") {
		t.Errorf("error should name the lost key, got %v", err)
	}
	assertUnchanged(t, path, original)
}

func TestBuildValidatedCatalogLostRegionFails(t *testing.T) {
	previous := map[string]map[string]float64{
		"westeurope":  {"Standard_B2s": 0.048},
		"northeurope": {"Standard_B2s": 0.046},
	}
	path, original := writeCatalog(t, t.TempDir(), "azure.yaml", previous)

	_, err := buildValidatedCatalog(catalogSpec{
		path:    path,
		regions: []string{"westeurope", "northeurope"},
		skus:    []string{"Standard_B2s"},
		fetch: fetchFrom(
			map[string]float64{"westeurope/Standard_B2s": 0.048},
			map[string]error{"northeurope/Standard_B2s": errors.New("region unreachable")},
		),
	})

	if err == nil {
		t.Fatal("expected a lost region to fail the refresh")
	}
	if !strings.Contains(err.Error(), "northeurope") {
		t.Errorf("error should name the lost region, got %v", err)
	}
	assertUnchanged(t, path, original)
}

func TestValidateContinuity(t *testing.T) {
	previous := map[string]map[string]float64{
		"westeurope":  {"Standard_B2s": 0.048, "Standard_D2as_v5": 0.104},
		"northeurope": {"Standard_B2s": 0.046},
	}
	generated := map[string]map[string]float64{
		"westeurope": {"Standard_B2s": 0.048},
	}

	err := validateContinuity(previous, generated)
	if err == nil {
		t.Fatal("expected a continuity error")
	}
	for _, want := range []string{"missing regions: northeurope", "missing keys: westeurope/Standard_D2as_v5"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q, got %v", want, err)
		}
	}

	if err := validateContinuity(previous, previous); err != nil {
		t.Errorf("identical catalogs must validate, got %v", err)
	}
}

func TestBuildValidatedCatalogKeepsUnrelatedValues(t *testing.T) {
	previous := map[string]map[string]float64{
		"westeurope": {
			"Standard_B2s":     0.048,
			"Standard_D2s_v3":  0.12,
			"Standard_D2as_v5": 0.196,
		},
	}
	path, _ := writeCatalog(t, t.TempDir(), "azure.yaml", previous)

	prices, err := buildValidatedCatalog(catalogSpec{
		path:    path,
		regions: []string{"westeurope"},
		skus:    []string{"Standard_B2s", "Standard_D2s_v3", "Standard_D2as_v5"},
		fetch: fetchFrom(map[string]float64{
			"westeurope/Standard_B2s":     0.048,
			"westeurope/Standard_D2s_v3":  0.12,
			"westeurope/Standard_D2as_v5": 0.104,
		}, nil),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := map[string]float64{"Standard_B2s": 0.048, "Standard_D2s_v3": 0.12, "Standard_D2as_v5": 0.104}
	for sku, price := range want {
		if prices["westeurope"][sku] != price {
			t.Errorf("%s = %v, want %v", sku, prices["westeurope"][sku], price)
		}
	}
	if len(prices["westeurope"]) != len(want) {
		t.Errorf("got %d keys, want %d", len(prices["westeurope"]), len(want))
	}
}

func TestRunRefreshWritesAfterBothProvidersSucceed(t *testing.T) {
	dir := t.TempDir()
	awsPath, _ := writeCatalog(t, dir, "aws.yaml", map[string]map[string]float64{"eu-west-2": {"r5.large": 0.148}})
	azurePath, _ := writeCatalog(t, dir, "azure.yaml", map[string]map[string]float64{"westeurope": {"Standard_D2as_v5": 0.104}})

	err := runRefresh([]catalogSpec{
		{path: awsPath, cloudName: "AWS EC2", unit: "instances", regions: []string{"eu-west-2"}, skus: []string{"r5.large"},
			fetch: fetchFrom(map[string]float64{"eu-west-2/r5.large": 0.148}, nil)},
		{path: azurePath, cloudName: "Azure VM", unit: "VM sizes", regions: []string{"westeurope"}, skus: []string{"Standard_D2as_v5"},
			fetch: fetchFrom(map[string]float64{"westeurope/Standard_D2as_v5": 0.104}, nil)},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for path, want := range map[string]string{awsPath: "r5.large: 0.148", azurePath: "Standard_D2as_v5: 0.104"} {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("read %s: %v", path, readErr)
		}
		if !strings.Contains(string(data), want) {
			t.Errorf("%s does not contain %q", filepath.Base(path), want)
		}
		if !strings.Contains(string(data), "DO NOT EDIT MANUALLY") {
			t.Errorf("%s was not rewritten by writeYAML", filepath.Base(path))
		}
	}
}

func TestRunRefreshAWSSucceedsAzureFails(t *testing.T) {
	dir := t.TempDir()
	awsPath, awsOriginal := writeCatalog(t, dir, "aws.yaml", map[string]map[string]float64{"eu-west-2": {"r5.large": 0.148}})
	azurePath, azureOriginal := writeCatalog(t, dir, "azure.yaml", map[string]map[string]float64{"westeurope": {"Standard_D2as_v5": 0.104}})

	err := runRefresh([]catalogSpec{
		{path: awsPath, cloudName: "AWS EC2", regions: []string{"eu-west-2"}, skus: []string{"r5.large"},
			fetch: fetchFrom(map[string]float64{"eu-west-2/r5.large": 0.148}, nil)},
		{path: azurePath, cloudName: "Azure VM", regions: []string{"westeurope"}, skus: []string{"Standard_D2as_v5"},
			fetch: fetchFrom(nil, map[string]error{"westeurope/Standard_D2as_v5": errors.New("selection error")})},
	})

	if err == nil {
		t.Fatal("expected the refresh to fail")
	}
	if !strings.Contains(err.Error(), "Azure VM") {
		t.Errorf("error should name the failing provider, got %v", err)
	}
	assertUnchanged(t, awsPath, awsOriginal)
	assertUnchanged(t, azurePath, azureOriginal)
}

func TestRunRefreshAzureSucceedsAWSFails(t *testing.T) {
	dir := t.TempDir()
	azurePath, azureOriginal := writeCatalog(t, dir, "azure.yaml", map[string]map[string]float64{"westeurope": {"Standard_D2as_v5": 0.104}})
	awsPath, awsOriginal := writeCatalog(t, dir, "aws.yaml", map[string]map[string]float64{"eu-west-2": {"r5.large": 0.148}})

	err := runRefresh([]catalogSpec{
		{path: azurePath, cloudName: "Azure VM", regions: []string{"westeurope"}, skus: []string{"Standard_D2as_v5"},
			fetch: fetchFrom(map[string]float64{"westeurope/Standard_D2as_v5": 0.104}, nil)},
		{path: awsPath, cloudName: "AWS EC2", regions: []string{"eu-west-2"}, skus: []string{"r5.large"},
			fetch: fetchFrom(nil, map[string]error{"eu-west-2/r5.large": errors.New("throttled")})},
	})

	if err == nil {
		t.Fatal("expected the refresh to fail")
	}
	if !strings.Contains(err.Error(), "AWS EC2") {
		t.Errorf("error should name the failing provider, got %v", err)
	}
	assertUnchanged(t, azurePath, azureOriginal)
	assertUnchanged(t, awsPath, awsOriginal)
}

func TestRunRefreshValidatesBeforeWriting(t *testing.T) {
	previous := map[string]map[string]float64{
		"westeurope": {"Standard_B2s": 0.048, "Standard_D2as_v5": 0.104},
	}
	path, original := writeCatalog(t, t.TempDir(), "azure.yaml", previous)

	err := runRefresh([]catalogSpec{{
		path:      path,
		cloudName: "Azure VM",
		regions:   []string{"westeurope"},
		skus:      []string{"Standard_B2s", "Standard_D2as_v5"},
		fetch: fetchFrom(
			map[string]float64{"westeurope/Standard_B2s": 0.048},
			map[string]error{"westeurope/Standard_D2as_v5": errors.New("selection error")},
		),
	}})

	if err == nil {
		t.Fatal("expected the refresh to fail")
	}
	assertUnchanged(t, path, original)
}

func TestLoadExistingCatalogMissingFile(t *testing.T) {
	if _, err := loadExistingCatalog(filepath.Join(t.TempDir(), "absent.yaml")); err == nil {
		t.Error("expected an error for a missing catalog")
	}
}

func TestLoadExistingCatalogEmpty(t *testing.T) {
	path, _ := writeCatalog(t, t.TempDir(), "azure.yaml", map[string]map[string]float64{})

	if _, err := loadExistingCatalog(path); err == nil {
		t.Error("expected an error for a catalog with no regions")
	}
}

func TestAWSFetchStopsOnCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	spec, err := awsSpec(ctx)
	if err != nil {
		t.Fatalf("awsSpec() error = %v", err)
	}
	cancel()

	prices, err := spec.fetch("eu-west-2", []string{"r5.large"})
	if err != context.Canceled {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if prices != nil {
		t.Errorf("prices = %v, want nil", prices)
	}
}

func azureClientAt(baseURL string) *azureClient {
	return &azureClient{
		http:       http.DefaultClient,
		baseURL:    baseURL,
		maxRetries: azureMaxRetries,
		retryWait:  time.Millisecond,
		retryCap:   time.Millisecond,
	}
}

func azurePageBody(next string, prices map[string]float64) string {
	var items []string
	for sku, price := range prices {
		name := strings.TrimPrefix(sku, "Standard_")
		items = append(items,
			fmt.Sprintf(`{"armSkuName":%q,"retailPrice":%g,"productName":"Virtual Machines Dsv5 Series Windows","skuName":%q,"type":"Consumption"}`, sku, price*4, name),
			fmt.Sprintf(`{"armSkuName":%q,"retailPrice":%g,"productName":"Virtual Machines Dsv5 Series","skuName":%q,"type":"Consumption"}`, sku, price, name),
		)
	}

	link := "null"
	if next != "" {
		link = fmt.Sprintf("%q", next)
	}
	return fmt.Sprintf(`{"Items":[%s],"NextPageLink":%s}`, strings.Join(items, ","), link)
}

func TestAzureClientReportsNonJSONBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, "The service is unavailable.")
	}))
	defer srv.Close()

	_, err := azureClientAt(srv.URL).prices(context.Background(), "westeurope", []string{"Standard_D2as_v5"})
	if err == nil {
		t.Fatal("expected an error for a non-200 response")
	}
	for _, want := range []string{"503", "The service is unavailable."} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "invalid character") {
		t.Errorf("body was parsed before the status was checked: %v", err)
	}
}

func TestAzureClientRetriesThrottledPage(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.Header().Set("x-ms-ratelimit-retailprices-retry-after", "60")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"Error":{"Code":"429","Message":"Too many requests."}}`)
			return
		}
		fmt.Fprint(w, azurePageBody("", map[string]float64{"Standard_D2as_v5": 0.104}))
	}))
	defer srv.Close()

	prices, err := azureClientAt(srv.URL).prices(context.Background(), "westeurope", []string{"Standard_D2as_v5"})
	if err != nil {
		t.Fatalf("a throttled page should be retried, got %v", err)
	}
	if prices["Standard_D2as_v5"] != 0.104 {
		t.Errorf("price = %v, want 0.104", prices["Standard_D2as_v5"])
	}
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Errorf("api calls = %d, want 2", n)
	}
}

func TestAzureClientGivesUpAfterMaxRetries(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"Error":{"Code":"429","Message":"Too many requests."}}`)
	}))
	defer srv.Close()

	_, err := azureClientAt(srv.URL).prices(context.Background(), "westeurope", []string{"Standard_D2as_v5"})
	if err == nil {
		t.Fatal("expected the refresh to fail once retries run out")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("error %q does not name the status", err)
	}
	if n := atomic.LoadInt32(&calls); n != azureMaxRetries+1 {
		t.Errorf("api calls = %d, want %d", n, azureMaxRetries+1)
	}
}

func TestAzureClientFetchesBatchInOneRequest(t *testing.T) {
	want := map[string]float64{"Standard_B2s": 0.0416, "Standard_D2as_v5": 0.104}

	var calls int32
	var gotFilter string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		gotFilter = r.URL.Query().Get("$filter")
		fmt.Fprint(w, azurePageBody("", want))
	}))
	defer srv.Close()

	prices, err := azureClientAt(srv.URL).prices(context.Background(), "westeurope", []string{"Standard_B2s", "Standard_D2as_v5"})
	if err != nil {
		t.Fatalf("prices() error = %v", err)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("api calls = %d, want 1 for the whole batch", n)
	}
	for sku, price := range want {
		if prices[sku] != price {
			t.Errorf("%s = %v, want %v", sku, prices[sku], price)
		}
	}
	for _, clause := range []string{
		"armRegionName eq 'westeurope'",
		"armSkuName eq 'Standard_B2s'",
		"armSkuName eq 'Standard_D2as_v5'",
	} {
		if !strings.Contains(gotFilter, clause) {
			t.Errorf("$filter missing %q, got %q", clause, gotFilter)
		}
	}
}

func TestAzureClientFollowsNextPageLink(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			fmt.Fprint(w, azurePageBody("http://"+r.Host+"/page2", map[string]float64{"Standard_B2s": 0.0416}))
			return
		}
		fmt.Fprint(w, azurePageBody("", map[string]float64{"Standard_D2as_v5": 0.104}))
	}))
	defer srv.Close()

	prices, err := azureClientAt(srv.URL).prices(context.Background(), "westeurope", []string{"Standard_B2s", "Standard_D2as_v5"})
	if err != nil {
		t.Fatalf("prices() error = %v", err)
	}
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Errorf("api calls = %d, want 2", n)
	}
	if prices["Standard_D2as_v5"] != 0.104 {
		t.Errorf("a price from the second page was dropped: %v", prices)
	}
}

func TestAzureRetryDelay(t *testing.T) {
	client := &azureClient{retryWait: azureRetryWait, retryCap: azureRetryCap}

	tests := []struct {
		name   string
		header http.Header
		want   time.Duration
	}{
		{"azure header", http.Header{"X-Ms-Ratelimit-Retailprices-Retry-After": {"30"}}, 30 * time.Second},
		{"standard header", http.Header{"Retry-After": {"5"}}, 5 * time.Second},
		{"capped", http.Header{"X-Ms-Ratelimit-Retailprices-Retry-After": {"600"}}, azureRetryCap},
		{"no header", http.Header{}, azureRetryWait},
	}

	for _, tt := range tests {
		if got := client.retryDelay(tt.header); got != tt.want {
			t.Errorf("%s: retryDelay() = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestAWSFetchesOneInstanceTypePerCall(t *testing.T) {
	spec, err := awsSpec(context.Background())
	if err != nil {
		t.Fatalf("awsSpec() error = %v", err)
	}
	if spec.batchSize != 1 {
		t.Errorf("batchSize = %d, want 1: a larger batch would discard prices already fetched when a later lookup fails", spec.batchSize)
	}
}
