package billing

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/costmanagement/armcostmanagement"
	"github.com/tanrikuluozlem/burn/internal/analyzer"
	"github.com/tanrikuluozlem/burn/internal/collector"
)

type AzureConfig struct {
	SubscriptionID string
	CostType       string // "actual" or "amortized" (default: amortized)
}

type AzureCostClient struct {
	client *armcostmanagement.QueryClient
	config AzureConfig
}

func NewAzureCostClient(ctx context.Context, cfg AzureConfig) (*AzureCostClient, error) {
	if cfg.SubscriptionID == "" {
		return nil, fmt.Errorf("Azure subscription ID required (--azure-subscription or AZURE_SUBSCRIPTION_ID)")
	}

	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return nil, fmt.Errorf("azure credentials: %w", err)
	}

	client, err := armcostmanagement.NewQueryClient(cred, nil)
	if err != nil {
		return nil, fmt.Errorf("azure cost client: %w", err)
	}

	return &AzureCostClient{
		client: client,
		config: cfg,
	}, nil
}

func (a *AzureCostClient) QueryCosts(ctx context.Context, start, end time.Time) ([]CURLineItem, error) {
	scope := fmt.Sprintf("/subscriptions/%s", a.config.SubscriptionID)

	timeframe := armcostmanagement.TimeframeTypeCustom
	costType := armcostmanagement.ExportTypeAmortizedCost
	if a.config.CostType == "actual" {
		costType = armcostmanagement.ExportTypeActualCost
	}
	startStr := start.Format("2006-01-02T00:00:00Z")
	// Azure Cost Management 'to' is inclusive — subtract 1 day for exclusive boundary
	endStr := end.Add(-24 * time.Hour).Format("2006-01-02T00:00:00Z")

	query := armcostmanagement.QueryDefinition{
		Type:      &costType,
		Timeframe: &timeframe,
		TimePeriod: &armcostmanagement.QueryTimePeriod{
			From: parseAzureTime(startStr),
			To:   parseAzureTime(endStr),
		},
		Dataset: &armcostmanagement.QueryDataset{
			// "None" granularity — aggregate totals per resource, no daily breakdown.
			// Reduces rows by Nx (N=days), avoiding the 5000-row API page limit.
			// Go SDK only defines GranularityTypeDaily, but the REST API accepts "None".
			Granularity: granularityNone(),
			Aggregation: map[string]*armcostmanagement.QueryAggregation{
				"Cost": {
					Name:     strPtr("Cost"),
					Function: funcPtr(armcostmanagement.FunctionTypeSum),
				},
			},
			Grouping: []*armcostmanagement.QueryGrouping{
				{
					Type: columnPtr(armcostmanagement.QueryColumnTypeDimension),
					Name: strPtr("ResourceId"),
				},
				{
					Type: columnPtr(armcostmanagement.QueryColumnTypeDimension),
					Name: strPtr("ResourceType"),
				},
				{
					Type: columnPtr(armcostmanagement.QueryColumnTypeDimension),
					Name: strPtr("MeterCategory"),
				},
				{
					Type: columnPtr(armcostmanagement.QueryColumnTypeDimension),
					Name: strPtr("PricingModel"),
				},
			},
		},
	}

	result, err := a.queryWithRetry(ctx, scope, query)
	if err != nil {
		return nil, err
	}

	items := parseAzureCostResult(result)

	// Azure Cost Management returns max 5000 rows per page.
	// The Go SDK doesn't handle nextLink pagination automatically.
	// For clusters with many resources over long periods, warn if truncated.
	if result.Properties != nil && result.Properties.NextLink != nil && *result.Properties.NextLink != "" {
		fmt.Printf("Warning: Azure cost query returned 5000+ rows — results may be truncated, try a shorter --days period\n")
	}

	return items, nil
}

func (a *AzureCostClient) queryWithRetry(ctx context.Context, scope string, query armcostmanagement.QueryDefinition) (armcostmanagement.QueryClientUsageResponse, error) {
	var result armcostmanagement.QueryClientUsageResponse
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		result, err = a.client.Usage(ctx, scope, query, nil)
		if err == nil {
			return result, nil
		}
		if strings.Contains(err.Error(), "429") && attempt < 2 {
			wait := time.Duration(60*(attempt+1)) * time.Second
			fmt.Printf("Rate limited, retrying in %v (%d/3)...\n", wait, attempt+2)
			time.Sleep(wait)
			continue
		}
		break
	}
	if strings.Contains(err.Error(), "NotFound") || strings.Contains(err.Error(), "offer type") {
		return result, fmt.Errorf("azure Cost Management API not available — this can happen with free trial/sponsorship subscriptions, or if a recent subscription upgrade has not propagated yet (wait 1-2 hours): %w", err)
	}
	return result, fmt.Errorf("azure cost query: %w", err)
}

func parseAzureCostResult(result armcostmanagement.QueryClientUsageResponse) []CURLineItem {
	var items []CURLineItem

	if result.Properties == nil || result.Properties.Rows == nil {
		slog.Debug("azure cost result: no rows")
		return items
	}

	// Build column index from response headers
	colIdx := make(map[string]int)
	for i, col := range result.Properties.Columns {
		if col.Name != nil {
			colIdx[strings.ToLower(*col.Name)] = i
		}
	}
	slog.Debug("azure cost columns", "mapping", colIdx)

	costCol, hasCost := colIdx["cost"]
	resIDCol, hasResID := colIdx["resourceid"]
	resTypeCol, hasResType := colIdx["resourcetype"]
	meterCol, hasMeter := colIdx["metercategory"]
	pricingCol, hasPricing := colIdx["pricingmodel"]

	if !hasCost || !hasResID {
		slog.Debug("azure cost result: missing required columns")
		return items
	}

	slog.Debug("azure cost result", "totalRows", len(result.Properties.Rows))
	for _, row := range result.Properties.Rows {
		cost, ok := toFloat64(row[costCol])
		if !ok {
			continue
		}

		resourceID, _ := row[resIDCol].(string)
		resourceType := ""
		if hasResType && resTypeCol < len(row) {
			resourceType, _ = row[resTypeCol].(string)
		}
		meterCategory := ""
		if hasMeter && meterCol < len(row) {
			meterCategory, _ = row[meterCol].(string)
		}

		pricingModel := ""
		if hasPricing && pricingCol < len(row) {
			pricingModel, _ = row[pricingCol].(string)
		}

		slog.Debug("azure cost row", "cost", cost, "resourceID", resourceID, "resourceType", resourceType, "meterCategory", meterCategory, "pricingModel", pricingModel)

		category := classifyAzureResource(resourceType, meterCategory)
		if category == "" {
			continue
		}

		usageType := "BoxUsage"
		mcLower := strings.ToLower(meterCategory)
		if strings.Contains(mcLower, "network") || strings.Contains(mcLower, "bandwidth") || strings.Contains(mcLower, "virtual network") {
			usageType = "DataTransfer"
		}

		pricingTerm := parsePricingModel(pricingModel)

		name := extractAzureResourceName(resourceID, category)

		items = append(items, CURLineItem{
			ResourceID:    name,
			EffectiveCost: cost,
			UsageType:     usageType,
			PricingTerm:   pricingTerm,
			InstanceType:  resourceType,
			Region:        "",
			Category:      category,
		})
	}

	return items
}

// ReconcileAzure runs the full Azure reconciliation flow.
func ReconcileAzure(ctx context.Context, client *AzureCostClient, nodes []collector.NodeInfo, estimatedCosts map[string]float64, namespaces []analyzer.NamespaceCost, pvcs []collector.PVCInfo, pvEstimates map[string]float64, lbs []collector.LBServiceInfo, lbEstimates map[string]float64, start, end time.Time, periodDays float64) (*ReconciliationReport, error) {
	allItems, err := client.QueryCosts(ctx, start, end)
	if err != nil {
		return nil, err
	}

	// Split items by category
	var computeItems, diskItems, networkItems []CURLineItem
	var managementCost float64
	for _, item := range allItems {
		switch item.Category {
		case CategoryCompute:
			computeItems = append(computeItems, item)
		case CategoryDisk:
			diskItems = append(diskItems, item)
		case CategoryNetwork:
			networkItems = append(networkItems, item)
		case CategoryManagement:
			managementCost += item.EffectiveCost
		}
	}

	curCosts := AggregateCURByResource(computeItems)
	matched, unmatchedCUR, unmatchedNodes := MatchNodesToCUR(nodes, estimatedCosts, curCosts, periodDays, start)

	// Match disk billing to PVCs
	var nodeNames []string
	for _, n := range nodes {
		nodeNames = append(nodeNames, n.Name)
	}
	diskCosts := AggregateCURByResource(diskItems)
	matchedDisks, orphanedDisks := MatchDisksToPVCs(pvcs, pvEstimates, diskCosts, nodeNames, periodDays)

	// Match network billing (LB + public IP)
	var lbItems, ipItems []CURLineItem
	for _, item := range networkItems {
		rt := strings.ToLower(item.InstanceType)
		if strings.Contains(rt, "loadbalancer") {
			lbItems = append(lbItems, item)
		} else {
			ipItems = append(ipItems, item)
		}
	}
	// Azure uses a shared LB ("kubernetes") for all LoadBalancer services.
	// Match by name first; if only one LB exists in both billing and K8s, pair them.
	lbCosts := AggregateCURByResource(lbItems)
	matchedLBs, orphanedLBs := MatchLBsToServices(lbs, lbEstimates, lbCosts, periodDays)

	// Public IPs
	var publicIPs []PublicIPReconciliation
	ipCosts := AggregateCURByResource(ipItems)
	for id, agg := range ipCosts {
		monthly := 0.0
		if periodDays > 0 {
			monthly = agg.TotalCost / periodDays * DaysPerMonth
		}
		publicIPs = append(publicIPs, PublicIPReconciliation{
			Name:       id,
			ActualCost: monthly,
		})
	}

	// Management fee (monthly projection)
	mgmtMonthly := 0.0
	if periodDays > 0 {
		mgmtMonthly = managementCost / periodDays * DaysPerMonth
	}

	var totalEst, totalActual float64
	var riCount, spCount, spotCount, odCount int
	var riSavings, spSavings, spotSavings float64

	for _, n := range matched {
		totalEst += n.EstimatedMonthlyCost
		totalActual += n.ActualCost

		switch n.PricingTerm {
		case "Reserved":
			riCount++
		case "SavingsPlan":
			spCount++
		case "Spot":
			spotCount++
		case "OnDemand":
			odCount++
		}

		saving := n.EstimatedMonthlyCost - n.ActualCost
		if saving <= 0 {
			continue
		}
		computeTotal := n.OnDemandCost + n.SPCost + n.RICost + n.SpotCost
		if computeTotal > 0 {
			spSavings += saving * n.SPCost / computeTotal
			riSavings += saving * n.RICost / computeTotal
			spotSavings += saving * n.SpotCost / computeTotal
		}
	}

	nsReconciliations := reconcileNamespaces(namespaces, totalEst, totalActual, nil, periodDays)

	coverageGaps := DetectCoverageGaps(matched)

	// Merge orphaned LBs into matchedLBs for report (all go in LoadBalancers)
	allLBs := append(matchedLBs, orphanedLBs...)

	// Infrastructure summary
	var diskEstTotal, diskActTotal, lbEstTotal, lbActTotal, ipActTotal float64
	for _, d := range matchedDisks {
		diskEstTotal += d.EstimatedCost
		diskActTotal += d.ActualCost
	}
	for _, d := range orphanedDisks {
		diskActTotal += d.ActualCost
	}
	for _, l := range allLBs {
		lbEstTotal += l.EstimatedCost
		lbActTotal += l.ActualCost
	}
	for _, p := range publicIPs {
		ipActTotal += p.ActualCost
	}

	infra := &InfrastructureSummary{
		ComputeEstimated: totalEst,
		ComputeActual:    totalActual,
		DiskEstimated:    diskEstTotal,
		DiskActual:       diskActTotal,
		LBEstimated:      lbEstTotal,
		LBActual:         lbActTotal,
		PublicIPActual:   ipActTotal,
		ManagementFee:    mgmtMonthly,
	}
	infra.TotalEstimated = infra.ComputeEstimated + infra.DiskEstimated + infra.LBEstimated
	infra.TotalActual = infra.ComputeActual + infra.DiskActual + infra.LBActual + infra.PublicIPActual + infra.ManagementFee

	infraDiff := infra.TotalActual - infra.TotalEstimated
	infraDiffPct := 0.0
	if infra.TotalEstimated > 0 {
		infraDiffPct = infraDiff / infra.TotalEstimated * 100
	}

	return &ReconciliationReport{
		GeneratedAt:        time.Now().UTC(),
		PeriodStart:        start,
		PeriodEnd:          end,
		DataDelay:          "Azure cost data delayed 8-24h (EA/MCA), up to 72h (PAYG)",
		TotalEstimatedCost: infra.TotalEstimated,
		TotalActualCost:    infra.TotalActual,
		TotalDifference:    infraDiff,
		TotalDiffPercent:   infraDiffPct,
		Nodes:              matched,
		Namespaces:         nsReconciliations,
		RINodeCount:        riCount,
		SPNodeCount:        spCount,
		SpotNodeCount:      spotCount,
		OnDemandNodeCount:  odCount,
		TotalRISavings:     math.Max(0, riSavings),
		TotalSPSavings:     math.Max(0, spSavings),
		TotalSpotSavings:   math.Max(0, spotSavings),
		UnmatchedCURItems:  unmatchedCUR,
		UnmatchedNodes:     unmatchedNodes,
		Disks:              matchedDisks,
		OrphanedDisks:      orphanedDisks,
		LoadBalancers:      allLBs,
		PublicIPs:          publicIPs,
		CoverageGaps:       coverageGaps,
		InfraCost:          infra,
	}, nil
}

// classifyAzureResource maps Azure resource types to billing categories.
func classifyAzureResource(resourceType, meterCategory string) string {
	rt := strings.ToLower(resourceType)
	mc := strings.ToLower(meterCategory)

	switch {
	case strings.Contains(rt, "virtualmachine") || strings.Contains(rt, "virtualmachinescaleset"):
		if mc == "storage" {
			return "" // VMSS-attached disks show as storage meter — skip, they're OS disks
		}
		return CategoryCompute
	case strings.Contains(rt, "disks"):
		return CategoryDisk
	case strings.Contains(rt, "loadbalancer"):
		return CategoryNetwork
	case strings.Contains(rt, "publicipaddress"):
		return CategoryNetwork
	case mc == "azure kubernetes service":
		return CategoryManagement
	default:
		return ""
	}
}

func parsePricingModel(pm string) string {
	switch strings.ToLower(pm) {
	case "reservation":
		return "Reserved"
	case "savingsplan":
		return "SavingsPlan"
	case "spot":
		return "Spot"
	default:
		return "OnDemand"
	}
}

// extractAzureResourceName extracts a usable name from an Azure resource ID.
// Compute resources use ParseProviderID (handles VMSS), others use the last path segment.
func extractAzureResourceName(resourceID, category string) string {
	if category == CategoryCompute {
		return ParseProviderID(resourceID)
	}
	parts := strings.Split(resourceID, "/")
	if len(parts) == 0 {
		return resourceID
	}
	return parts[len(parts)-1]
}

func parseAzureTime(s string) *time.Time {
	t, _ := time.Parse("2006-01-02T15:04:05Z", s)
	return &t
}

func toFloat64(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case string:
		var f float64
		if _, err := fmt.Sscanf(n, "%f", &f); err == nil {
			return f, true
		}
		return 0, false
	default:
		return 0, false
	}
}

func granularityNone() *armcostmanagement.GranularityType {
	g := armcostmanagement.GranularityType("None")
	return &g
}

func strPtr(s string) *string { return &s }
func funcPtr(f armcostmanagement.FunctionType) *armcostmanagement.FunctionType { return &f }
func columnPtr(c armcostmanagement.QueryColumnType) *armcostmanagement.QueryColumnType { return &c }
