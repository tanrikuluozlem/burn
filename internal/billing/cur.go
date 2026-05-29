package billing

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/tanrikuluozlem/burn/internal/collector"
)

var validIdentifier = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

func ParseProviderID(providerID string) string {
	if providerID == "" {
		return ""
	}
	parts := strings.Split(providerID, "/")
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}

func BuildNodeInstanceMap(nodes []collector.NodeInfo) map[string]int {
	m := make(map[string]int)
	for i, n := range nodes {
		instanceID := ParseProviderID(n.ProviderID)
		if instanceID != "" {
			m[instanceID] = i
		}
	}
	return m
}

func AggregateCURByResource(items []CURLineItem) map[string]*AggregatedCost {
	m := make(map[string]*AggregatedCost)
	for _, item := range items {
		if item.ResourceID == "" {
			continue
		}
		agg, ok := m[item.ResourceID]
		if !ok {
			agg = &AggregatedCost{ResourceID: item.ResourceID}
			m[item.ResourceID] = agg
		}

		agg.TotalCost += item.EffectiveCost

		isDataTransfer := strings.Contains(item.UsageType, "DataTransfer") ||
			strings.Contains(item.UsageType, "NatGateway") ||
			strings.Contains(item.UsageType, "Bytes")
		if isDataTransfer {
			agg.DataTransferCost += item.EffectiveCost
		} else {
			agg.ComputeCost += item.EffectiveCost
			agg.UsageHours += item.UsageAmount
		}

		switch {
		case strings.Contains(item.UsageType, "SpotUsage"):
			agg.SpotCost += item.EffectiveCost
		case item.ReservationARN != "" || item.PricingTerm == "Reserved":
			agg.RICost += item.EffectiveCost
		case item.SavingsPlanARN != "" || strings.Contains(item.PricingTerm, "Saving"):
			agg.SPCost += item.EffectiveCost
		default:
			agg.OnDemandCost += item.EffectiveCost
		}
	}

	for _, agg := range m {
		switch {
		case agg.RICost > agg.OnDemandCost && agg.RICost > agg.SPCost:
			agg.PricingTerm = "Reserved"
		case agg.SPCost > agg.OnDemandCost:
			agg.PricingTerm = "SavingsPlan"
		case agg.SpotCost > 0:
			agg.PricingTerm = "Spot"
		default:
			agg.PricingTerm = "OnDemand"
		}
	}

	return m
}

func MatchNodesToCUR(
	nodes []collector.NodeInfo,
	estimatedCosts map[string]float64,
	curCosts map[string]*AggregatedCost,
	periodDays float64,
) ([]NodeReconciliation, int, int) {
	instanceMap := BuildNodeInstanceMap(nodes)

	matched := make(map[int]bool)
	matchedCUR := make(map[string]bool)
	var results []NodeReconciliation

	for resourceID, agg := range curCosts {
		idx, ok := instanceMap[resourceID]
		if !ok {
			continue
		}

		node := nodes[idx]
		matched[idx] = true
		matchedCUR[resourceID] = true

		var monthlyActual, monthlyCompute, monthlyTransfer float64
		if periodDays > 0 {
			monthlyActual = agg.TotalCost / periodDays * 30.44
			monthlyCompute = agg.ComputeCost / periodDays * 30.44
			monthlyTransfer = agg.DataTransferCost / periodDays * 30.44
		}

		estimated := estimatedCosts[node.Name]
		diff := monthlyActual - estimated
		diffPercent := 0.0
		if estimated > 0 {
			diffPercent = diff / estimated * 100
		}

		alert := ""
		if diffPercent > 20 {
			alert = fmt.Sprintf("cost %+.0f%% over estimate — check for missing RI/SP coverage", diffPercent)
		} else if diffPercent < -30 {
			alert = fmt.Sprintf("cost %+.0f%% under estimate — possible RI/SP not reflected in pricing", diffPercent)
		}

		results = append(results, NodeReconciliation{
			NodeName:             node.Name,
			InstanceID:           resourceID,
			InstanceType:         node.InstanceType,
			Region:               node.Region,
			IsSpot:               node.IsSpot,
			EstimatedMonthlyCost: estimated,
			ActualCost:           monthlyActual,
			ActualComputeCost:    monthlyCompute,
			ActualTransferCost:   monthlyTransfer,
			ActualHours:          agg.UsageHours,
			PricingTerm:          agg.PricingTerm,
			CostDifference:       diff,
			DifferencePercent:    diffPercent,
			MatchMethod:          "provider-id",
			DriftAlert:           alert,
		})
	}

	unmatchedCUR := 0
	for id := range curCosts {
		if !matchedCUR[id] {
			unmatchedCUR++
		}
	}

	unmatchedNodes := 0
	for i := range nodes {
		if !matched[i] {
			unmatchedNodes++
		}
	}

	return results, unmatchedCUR, unmatchedNodes
}

func ValidateAthenaConfig(cfg AthenaConfig) error {
	if cfg.Database == "" {
		return fmt.Errorf("CUR database required (--cur-database or CUR_DATABASE)")
	}
	if !validIdentifier.MatchString(cfg.Database) {
		return fmt.Errorf("invalid database name: %q", cfg.Database)
	}
	if cfg.Table == "" {
		return fmt.Errorf("CUR table required (--cur-table or CUR_TABLE)")
	}
	if !validIdentifier.MatchString(cfg.Table) {
		return fmt.Errorf("invalid table name: %q", cfg.Table)
	}
	if cfg.OutputLocation == "" {
		return fmt.Errorf("Athena output location required (--cur-output or CUR_OUTPUT_LOCATION)")
	}
	if !strings.HasPrefix(cfg.OutputLocation, "s3://") {
		return fmt.Errorf("output location must start with s3://")
	}
	return nil
}
