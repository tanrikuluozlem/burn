package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

func fetchFrom(prices map[string]float64, failures map[string]error) func(string, string) (float64, error) {
	return func(region, sku string) (float64, error) {
		key := region + "/" + sku
		if err, ok := failures[key]; ok {
			return 0, err
		}
		if price, ok := prices[key]; ok {
			return price, nil
		}
		return 0, fmt.Errorf("no price for %s", key)
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

	price, err := spec.fetch("eu-west-2", "r5.large")
	if err != context.Canceled {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if price != 0 {
		t.Errorf("price = %v, want 0", price)
	}
}
