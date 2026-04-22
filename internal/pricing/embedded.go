package pricing

import (
	"embed"
	"fmt"
	"sync"

	"gopkg.in/yaml.v3"
)

//go:embed data/*.yaml
var priceData embed.FS

type priceDB struct {
	aws   map[string]map[string]float64
	azure map[string]map[string]float64
	gcp   map[string]map[string]float64
	mu    sync.RWMutex
}

var db *priceDB
var dbOnce sync.Once

func loadPriceDB() *priceDB {
	dbOnce.Do(func() {
		db = &priceDB{
			aws:   make(map[string]map[string]float64),
			azure: make(map[string]map[string]float64),
			gcp:   make(map[string]map[string]float64),
		}
		db.aws = loadYAML("data/aws.yaml")
		db.azure = loadYAML("data/azure.yaml")
		db.gcp = loadYAML("data/gcp.yaml")
	})
	return db
}

func loadYAML(path string) map[string]map[string]float64 {
	data, err := priceData.ReadFile(path)
	if err != nil {
		return nil
	}

	var prices map[string]map[string]float64
	if err := yaml.Unmarshal(data, &prices); err != nil {
		return nil
	}
	return prices
}

func (p *priceDB) getAWSPrice(region, instanceType string) (float64, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.aws == nil {
		return 0, false
	}
	regionPrices, ok := p.aws[region]
	if !ok {
		regionPrices, ok = p.aws["us-east-1"]
		if !ok {
			return 0, false
		}
	}
	price, ok := regionPrices[instanceType]
	return price, ok
}

func (p *priceDB) getAzurePrice(region, vmSize string) (float64, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.azure == nil {
		return 0, false
	}
	regionPrices, ok := p.azure[region]
	if !ok {
		regionPrices, ok = p.azure["eastus"]
		if !ok {
			return 0, false
		}
	}
	price, ok := regionPrices[vmSize]
	return price, ok
}

func (p *priceDB) getGCPPrice(region, machineType string) (float64, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.gcp == nil {
		return 0, false
	}
	regionPrices, ok := p.gcp[region]
	if !ok {
		regionPrices, ok = p.gcp["us-central1"]
		if !ok {
			return 0, false
		}
	}
	price, ok := regionPrices[machineType]
	return price, ok
}

// GetEmbeddedPrice returns the price from embedded YAML data
func GetEmbeddedPrice(cloud, region, instanceType string) (float64, error) {
	db := loadPriceDB()

	var price float64
	var ok bool

	switch cloud {
	case "aws":
		price, ok = db.getAWSPrice(region, instanceType)
	case "azure":
		price, ok = db.getAzurePrice(region, instanceType)
	case "gcp":
		price, ok = db.getGCPPrice(region, instanceType)
	default:
		return 0, fmt.Errorf("unknown cloud: %s", cloud)
	}

	if !ok {
		return 0, fmt.Errorf("price not found for %s/%s/%s", cloud, region, instanceType)
	}
	return price, nil
}
