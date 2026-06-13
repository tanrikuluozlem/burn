package billing

import (
	"fmt"
	"regexp"
	"strings"
	"time"

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
		case strings.Contains(item.UsageType, "SpotUsage") || item.PricingTerm == "Spot":
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
		case agg.SpotCost > 0:
			agg.PricingTerm = "Spot"
		case agg.RICost > 0:
			agg.PricingTerm = "Reserved"
		case agg.SPCost > 0:
			agg.PricingTerm = "SavingsPlan"
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
	periodStart time.Time,
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

			result := buildNodeReconciliation(node, resourceID, agg, estimatedCosts[node.Name], periodDays, "provider-id", periodStart)
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
				result := buildNodeReconciliation(node, resourceID, splitAgg, estimatedCosts[node.Name], periodDays, "vmss-split", periodStart)
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

func buildNodeReconciliation(node collector.NodeInfo, resourceID string, agg *AggregatedCost, estimated, periodDays float64, matchMethod string, periodStart time.Time) NodeReconciliation {
	var monthlyActual, monthlyCompute, monthlyTransfer float64
	var odCost, spotCost, riCost, spCost float64
	if periodDays > 0 {
		monthlyActual = agg.TotalCost / periodDays * DaysPerMonth
		monthlyCompute = agg.ComputeCost / periodDays * DaysPerMonth
		monthlyTransfer = agg.DataTransferCost / periodDays * DaysPerMonth
		odCost = agg.OnDemandCost / periodDays * DaysPerMonth
		spotCost = agg.SpotCost / periodDays * DaysPerMonth
		riCost = agg.RICost / periodDays * DaysPerMonth
		spCost = agg.SPCost / periodDays * DaysPerMonth
	}

	diff := monthlyActual - estimated
	diffPercent := 0.0
	if estimated > 0 {
		diffPercent = diff / estimated * 100
	}

	// Detect partial period — node was created during the billing window.
	// Don't adjust the projection; instead, explain the drift to the user.
	partialPeriod := !node.CreatedAt.IsZero() && node.CreatedAt.After(periodStart)

	alert := ""
	if partialPeriod && (diffPercent > 20 || diffPercent < -30) {
		alert = "node created during billing period — actual cost reflects partial uptime, not a pricing issue"
	} else if diffPercent > 20 {
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
		OnDemandCost:         odCost,
		SpotCost:             spotCost,
		RICost:               riCost,
		SPCost:               spCost,
		CostDifference:       diff,
		DifferencePercent:    diffPercent,
		MatchMethod:          matchMethod,
		DriftAlert:           alert,
	}
}

// MatchDisksToPVCs matches billing disk costs to K8s PVCs by cloud volume ID.
// OS disks are identified and returned separately from orphaned data disks.
func MatchDisksToPVCs(
	pvcs []collector.PVCInfo,
	pvEstimates map[string]float64, // key: "namespace/name"
	billingDisks map[string]*AggregatedCost,
	nodeNames []string,
	periodDays float64,
) (matched []DiskReconciliation, orphaned []DiskReconciliation) {
	// Build PVC lookup by cloud disk ID
	type pvcEntry struct {
		info     collector.PVCInfo
		estimate float64
	}
	pvcByDisk := make(map[string]pvcEntry)
	for _, pvc := range pvcs {
		if pvc.CloudDiskID == "" {
			continue
		}
		key := pvc.Namespace + "/" + pvc.Name
		entry := pvcEntry{info: pvc, estimate: pvEstimates[key]}
		pvcByDisk[pvc.CloudDiskID] = entry
		// Azure CSI uses full resource path as volume handle.
		// Billing uses the short disk name (last path segment).
		// Index both so matching works regardless of format.
		parts := strings.Split(pvc.CloudDiskID, "/")
		if short := parts[len(parts)-1]; short != pvc.CloudDiskID {
			pvcByDisk[short] = entry
		}
	}

	for diskID, agg := range billingDisks {
		monthly := 0.0
		if periodDays > 0 {
			monthly = agg.TotalCost / periodDays * DaysPerMonth
		}

		// Check PVC match
		pvc, found := pvcByDisk[diskID]
		if found {
			diff := monthly - pvc.estimate
			diffPct := 0.0
			if pvc.estimate > 0 {
				diffPct = diff / pvc.estimate * 100
			}
			matched = append(matched, DiskReconciliation{
				DiskName:      diskID,
				PVCName:       pvc.info.Name,
				PVCNamespace:  pvc.info.Namespace,
				StorageClass:  pvc.info.StorageClass,
				CapacityGiB:   float64(pvc.info.RequestedBytes) / (1024 * 1024 * 1024),
				EstimatedCost: pvc.estimate,
				ActualCost:    monthly,
				Difference:    diff,
				DiffPercent:   diffPct,
				MatchMethod:   "volume-id",
			})
			continue
		}

		// Check if this is a node OS disk (not orphaned, just infrastructure)
		if isOSDisk(diskID, nodeNames) {
			matched = append(matched, DiskReconciliation{
				DiskName:    diskID,
				PVCName:     "(OS disk)",
				ActualCost:  monthly,
				MatchMethod: "os-disk",
			})
			continue
		}

		orphaned = append(orphaned, DiskReconciliation{
			DiskName:    diskID,
			ActualCost:  monthly,
			IsOrphaned:  true,
			MatchMethod: "unmatched",
		})
	}
	return matched, orphaned
}

// isOSDisk checks if a billing disk name belongs to a node.
// Azure OS disks contain the node pool name or "os" in their resource name.
// AWS EBS root volumes are attached to instances (matched by DescribeVolumes, not here).
func isOSDisk(diskID string, nodeNames []string) bool {
	lower := strings.ToLower(diskID)
	if strings.Contains(lower, "os_") || strings.Contains(lower, "_osdisk") || strings.Contains(lower, "disk1_") {
		return true
	}
	for _, name := range nodeNames {
		if strings.Contains(lower, strings.ToLower(name)) {
			return true
		}
	}
	return false
}

// MatchLBsToServices matches billing LB costs to K8s LoadBalancer services.
func MatchLBsToServices(
	services []collector.LBServiceInfo,
	lbEstimates map[string]float64, // key: "namespace/name"
	billingLBs map[string]*AggregatedCost,
	periodDays float64,
) (matched []LBReconciliation, orphaned []LBReconciliation) {
	// Build service lookup by hostname
	type svcEntry struct {
		info     collector.LBServiceInfo
		estimate float64
	}
	svcByHost := make(map[string]svcEntry)
	svcByName := make(map[string]svcEntry)
	for _, svc := range services {
		key := svc.Namespace + "/" + svc.Name
		entry := svcEntry{info: svc, estimate: lbEstimates[key]}
		if svc.Hostname != "" {
			svcByHost[svc.Hostname] = entry
		}
		svcByName[strings.ToLower(svc.Name)] = entry
	}

	for lbID, agg := range billingLBs {
		monthly := 0.0
		if periodDays > 0 {
			monthly = agg.TotalCost / periodDays * DaysPerMonth
		}

		// Try hostname match first, then name match.
		// AKS uses a shared LB named "kubernetes" — if billing has "kubernetes"
		// and only one K8s LoadBalancer service exists, match them.
		var svc *svcEntry
		method := "unmatched"
		if s, ok := svcByHost[lbID]; ok {
			svc = &s
			method = "hostname"
		} else if s, ok := svcByName[strings.ToLower(lbID)]; ok {
			svc = &s
			method = "name"
		} else if strings.ToLower(lbID) == "kubernetes" && len(services) > 0 {
			splitCost := monthly / float64(len(services))
			for _, s := range services {
				key := s.Namespace + "/" + s.Name
				est := lbEstimates[key]
				diff := splitCost - est
				diffPct := 0.0
				if est > 0 {
					diffPct = diff / est * 100
				}
				matched = append(matched, LBReconciliation{
					LBName:           lbID,
					ServiceName:      s.Name,
					ServiceNamespace: s.Namespace,
					EstimatedCost:    est,
					ActualCost:       splitCost,
					Difference:       diff,
					DiffPercent:      diffPct,
					MatchMethod:      "aks-shared-lb",
				})
			}
			continue
		}

		if svc != nil {
			diff := monthly - svc.estimate
			diffPct := 0.0
			if svc.estimate > 0 {
				diffPct = diff / svc.estimate * 100
			}
			matched = append(matched, LBReconciliation{
				LBName:           lbID,
				ServiceName:      svc.info.Name,
				ServiceNamespace: svc.info.Namespace,
				EstimatedCost:    svc.estimate,
				ActualCost:       monthly,
				Difference:       diff,
				DiffPercent:      diffPct,
				MatchMethod:      method,
			})
		} else {
			orphaned = append(orphaned, LBReconciliation{
				LBName:      lbID,
				ActualCost:  monthly,
				IsOrphaned:  true,
				MatchMethod: "unmatched",
			})
		}
	}
	return matched, orphaned
}

// DetectCoverageGaps finds on-demand nodes that could benefit from RI/SP.
func DetectCoverageGaps(nodes []NodeReconciliation) []CoverageGap {
	var gaps []CoverageGap
	for _, n := range nodes {
		if n.PricingTerm != "OnDemand" || n.SPCost > 0 || n.RICost > 0 || n.SpotCost > 0 {
			continue
		}
		if n.ActualCost < 50 {
			continue
		}
		saving := n.ActualCost * 0.30
		gaps = append(gaps, CoverageGap{
			NodeName:        n.NodeName,
			InstanceType:    n.InstanceType,
			Region:          n.Region,
			MonthlyCost:     n.ActualCost,
			PotentialSaving: saving,
			Recommendation:  fmt.Sprintf("$%.0f/mo with 1yr Reserved Instance", saving),
		})
	}
	return gaps
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
