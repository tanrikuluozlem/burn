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

	// Azure VMSS: .../virtualMachineScaleSets/{vmss-name}/virtualMachines/{id}
	// Return "{vmss-name}/{id}" to avoid collisions across node pools
	for i, p := range parts {
		if strings.EqualFold(p, "virtualMachineScaleSets") && i+3 < len(parts) &&
			strings.EqualFold(parts[i+2], "virtualMachines") {
			return parts[i+1] + "/" + parts[i+3]
		}
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
		if ok {
			// Exact match (AWS instance ID or Azure individual VM)
			node := nodes[idx]
			matched[idx] = true
			matchedCUR[resourceID] = true

			result := buildNodeReconciliation(node, resourceID, agg, estimatedCosts[node.Name], periodDays, "provider-id")
			results = append(results, result)
			continue
		}

		// VMSS fallback: Azure reports costs at scale set level, not per-VM.
		// Find all nodes whose instance key starts with "resourceID/"
		var vmssNodes []int
		for key, nodeIdx := range instanceMap {
			if strings.HasPrefix(key, resourceID+"/") {
				vmssNodes = append(vmssNodes, nodeIdx)
			}
		}
		if len(vmssNodes) > 0 {
			matchedCUR[resourceID] = true
			// Split cost evenly across VMSS nodes
			splitAgg := &AggregatedCost{
				ResourceID:       agg.ResourceID,
				TotalCost:        agg.TotalCost / float64(len(vmssNodes)),
				ComputeCost:      agg.ComputeCost / float64(len(vmssNodes)),
				DataTransferCost: agg.DataTransferCost / float64(len(vmssNodes)),
				UsageHours:       agg.UsageHours / float64(len(vmssNodes)),
				PricingTerm:      agg.PricingTerm,
				OnDemandCost:     agg.OnDemandCost / float64(len(vmssNodes)),
				RICost:           agg.RICost / float64(len(vmssNodes)),
				SPCost:           agg.SPCost / float64(len(vmssNodes)),
				SpotCost:         agg.SpotCost / float64(len(vmssNodes)),
			}
			for _, nodeIdx := range vmssNodes {
				node := nodes[nodeIdx]
				matched[nodeIdx] = true
				result := buildNodeReconciliation(node, resourceID, splitAgg, estimatedCosts[node.Name], periodDays, "vmss-split")
				results = append(results, result)
			}
		}
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

func buildNodeReconciliation(node collector.NodeInfo, resourceID string, agg *AggregatedCost, estimated, periodDays float64, matchMethod string) NodeReconciliation {
	var monthlyActual, monthlyCompute, monthlyTransfer float64
	if periodDays > 0 {
		monthlyActual = agg.TotalCost / periodDays * (730.0 / 24.0)
		monthlyCompute = agg.ComputeCost / periodDays * (730.0 / 24.0)
		monthlyTransfer = agg.DataTransferCost / periodDays * (730.0 / 24.0)
	}

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

	return NodeReconciliation{
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
		MatchMethod:          matchMethod,
		DriftAlert:           alert,
	}
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
