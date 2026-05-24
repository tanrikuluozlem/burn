package pricing

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

const spotAdvisorURL = "https://spot-bid-advisor.s3.amazonaws.com/spot-advisor-data.json"

type spotAdvisorData struct {
	SpotAdvisor map[string]map[string]map[string]struct {
		S int `json:"s"`
		R int `json:"r"`
	} `json:"spot_advisor"`
}

var (
	advisorCache   *spotAdvisorData
	advisorExpires time.Time
	advisorMu      sync.Mutex
)

func fetchSpotAdvisor() (*spotAdvisorData, error) {
	advisorMu.Lock()
	defer advisorMu.Unlock()

	if advisorCache != nil && time.Now().Before(advisorExpires) {
		return advisorCache, nil
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(spotAdvisorURL)
	if err != nil {
		return advisorCache, fmt.Errorf("spot advisor fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return advisorCache, fmt.Errorf("spot advisor: status %d", resp.StatusCode)
	}

	var data spotAdvisorData
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return advisorCache, fmt.Errorf("spot advisor decode: %w", err)
	}

	advisorCache = &data
	advisorExpires = time.Now().Add(1 * time.Hour)
	return advisorCache, nil
}

func lookupSpotAdvisor(instanceType, region string) (savings int, interruption int, ok bool) {
	data, err := fetchSpotAdvisor()
	if err != nil || data == nil {
		return 0, 0, false
	}

	regionData, exists := data.SpotAdvisor[region]
	if !exists {
		return 0, 0, false
	}

	linuxData, exists := regionData["Linux"]
	if !exists {
		return 0, 0, false
	}

	entry, exists := linuxData[instanceType]
	if !exists {
		return 0, 0, false
	}

	return entry.S, entry.R, true
}
