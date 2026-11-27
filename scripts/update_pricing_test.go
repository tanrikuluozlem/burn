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

	"golang.org/x/time/rate"
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
		limiter:    rate.NewLimiter(rate.Inf, 1),
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
			w.Header().Set("x-ms-ratelimit-retailprices-retry-after", "1")
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

func TestAzureClientPacesRequests(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, azurePageBody("", map[string]float64{"Standard_B2s": 0.0416}))
	}))
	defer srv.Close()

	client := azureClientAt(srv.URL)
	client.limiter = rate.NewLimiter(rate.Every(40*time.Millisecond), 1)

	start := time.Now()
	for range 3 {
		if _, _, err := client.request(context.Background(), srv.URL); err != nil {
			t.Fatalf("request() error = %v", err)
		}
	}

	// The burst covers the first call, so the other two wait one interval each.
	if elapsed := time.Since(start); elapsed < 80*time.Millisecond {
		t.Errorf("three requests took %v, want at least 80ms", elapsed)
	}
}

func TestAzureClientStopsOnCancelledContext(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		fmt.Fprint(w, azurePageBody("", map[string]float64{"Standard_B2s": 0.0416}))
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := azureClientAt(srv.URL).prices(ctx, "westeurope", []string{"Standard_B2s"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("prices() error = %v, want context.Canceled", err)
	}
	if n := atomic.LoadInt32(&calls); n != 0 {
		t.Errorf("api calls = %d, want none after cancellation", n)
	}
}

func TestRunRefreshKeepsDataWhenAzureOmitsSKU(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, azurePageBody("", map[string]float64{"Standard_B2s": 0.0416}))
	}))
	defer srv.Close()

	path, original := writeCatalog(t, t.TempDir(), "azure.yaml", map[string]map[string]float64{
		"westeurope": {"Standard_B2s": 0.0416, "Standard_D2as_v5": 0.104},
	})

	client := azureClientAt(srv.URL)
	err := runRefresh([]catalogSpec{{
		path: path, cloudName: "Azure VM", unit: "VM sizes",
		regions: []string{"westeurope"}, skus: []string{"Standard_B2s", "Standard_D2as_v5"},
		batchSize: azureBatchSize,
		fetch: func(region string, skus []string) (map[string]float64, error) {
			return client.prices(context.Background(), region, skus)
		},
	}})

	if err == nil {
		t.Fatal("expected a missing SKU to fail the refresh")
	}
	assertUnchanged(t, path, original)
}

func throttlingServer(t *testing.T, calls *int32, hit chan<- struct{}) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(calls, 1)
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusTooManyRequests)
		if hit != nil {
			select {
			case hit <- struct{}{}:
			default:
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAzureClientStopsWhileWaitingToRetry(t *testing.T) {
	var calls int32
	hit := make(chan struct{}, 1)
	srv := throttlingServer(t, &calls, hit)

	// No deadline, so the client reaches the retry wait.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := azureClientAt(srv.URL).prices(ctx, "westeurope", []string{"Standard_B2s"})
		done <- err
	}()

	<-hit
	select {
	case err := <-done:
		t.Fatalf("prices() returned before cancellation: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("prices() error = %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancellation did not interrupt the retry wait")
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("api calls = %d, want 1", n)
	}
}

func TestAzureClientFailsWhenRetryExceedsBudget(t *testing.T) {
	var calls int32
	srv := throttlingServer(t, &calls, nil)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err := azureClientAt(srv.URL).prices(ctx, "westeurope", []string{"Standard_B2s"})
	if err == nil {
		t.Fatal("expected the refresh to fail")
	}
	// The budget error distinguishes giving up from sleeping until the deadline.
	if !strings.Contains(err.Error(), "budget") {
		t.Errorf("error %q does not report the budget shortfall", err)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("api calls = %d, want 1 with no early retry", n)
	}
}

func TestRunRefreshKeepsDataWhenAzureStaysThrottled(t *testing.T) {
	var calls int32
	srv := throttlingServer(t, &calls, nil)

	path, original := writeCatalog(t, t.TempDir(), "azure.yaml", map[string]map[string]float64{
		"westeurope": {"Standard_B2s": 0.0416},
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	client := azureClientAt(srv.URL)
	err := runRefresh([]catalogSpec{{
		path: path, cloudName: "Azure VM", unit: "VM sizes",
		regions: []string{"westeurope"}, skus: []string{"Standard_B2s"},
		batchSize: azureBatchSize,
		fetch: func(region string, skus []string) (map[string]float64, error) {
			return client.prices(ctx, region, skus)
		},
	}})

	if err == nil {
		t.Fatal("expected sustained throttling to fail the refresh")
	}
	assertUnchanged(t, path, original)
}

func TestAzureRetryDelayHTTPDate(t *testing.T) {
	client := &azureClient{retryWait: azureRetryWait, limiter: rate.NewLimiter(rate.Inf, 1)}

	// http.TimeFormat drops sub-second precision, so the delay lands just under.
	future := http.Header{"Retry-After": {time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)}}
	if got := client.retryDelay(future); got <= 59*time.Minute || got > time.Hour {
		t.Errorf("future date = %v, want just under an hour", got)
	}

	past := http.Header{"Retry-After": {time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)}}
	if got := client.retryDelay(past); got != 0 {
		t.Errorf("past date = %v, want 0 so the retry happens now", got)
	}
}

func TestAzureClientDoesNotRetryUnusableRetryAfter(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Retry-After", "99999999999")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	_, err := azureClientAt(srv.URL).prices(context.Background(), "westeurope", []string{"Standard_B2s"})
	if err == nil {
		t.Fatal("expected an unusable retry-after to fail the refresh")
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("api calls = %d, want 1 with no retry", n)
	}
}

func TestAzureClientStopsWhileWaitingForLimiter(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		fmt.Fprint(w, azurePageBody("", map[string]float64{"Standard_B2s": 0.0416}))
	}))
	defer srv.Close()

	client := azureClientAt(srv.URL)
	// One token, then an hour: the second request blocks inside the limiter.
	client.limiter = rate.NewLimiter(rate.Every(time.Hour), 1)

	ctx, cancel := context.WithCancel(context.Background())
	if _, _, err := client.request(ctx, srv.URL); err != nil {
		t.Fatalf("first request() error = %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, _, err := client.request(ctx, srv.URL)
		done <- err
	}()

	// Without pacing this call would reach the server instead of blocking.
	select {
	case err := <-done:
		t.Fatalf("request() returned without waiting for the limiter: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("request() error = %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancellation did not interrupt the limiter wait")
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("api calls = %d, want 1", n)
	}
}

func TestRefreshTimeout(t *testing.T) {
	want := map[string]time.Duration{
		"azure": 12 * time.Minute,
		"aws":   10 * time.Minute,
		"all":   10 * time.Minute,
		"gcp":   10 * time.Minute,
	}
	for target, d := range want {
		if got := refreshTimeout(target); got != d {
			t.Errorf("refreshTimeout(%q) = %v, want %v", target, got, d)
		}
	}
}

func TestAzureRetryDelay(t *testing.T) {
	client := &azureClient{retryWait: azureRetryWait, limiter: rate.NewLimiter(rate.Inf, 1)}

	tests := []struct {
		name   string
		header http.Header
		want   time.Duration
	}{
		{"azure header", http.Header{"X-Ms-Ratelimit-Retailprices-Retry-After": {"30"}}, 30 * time.Second},
		{"standard header", http.Header{"Retry-After": {"5"}}, 5 * time.Second},
		{"above a minute is not truncated", http.Header{"X-Ms-Ratelimit-Retailprices-Retry-After": {"600"}}, 600 * time.Second},
		{"no header", http.Header{}, azureRetryWait},
		{"not a number", http.Header{"Retry-After": {"later"}}, azureRetryWait},
		{"negative", http.Header{"Retry-After": {"-5"}}, azureRetryWait},
		{"zero is a delay, not a missing header", http.Header{"Retry-After": {"0"}}, 0},
		{"overflows a duration", http.Header{"Retry-After": {"99999999999"}}, noRetry},
		{"overflows an int", http.Header{"Retry-After": {"99999999999999999999999"}}, noRetry},
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
