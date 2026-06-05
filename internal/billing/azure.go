package billing

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/costmanagement/armcostmanagement"
	"github.com/tanrikuluozlem/burn/internal/analyzer"
	"github.com/tanrikuluozlem/burn/internal/collector"
)

type AzureConfig struct {
	SubscriptionID string
}

type AzureCostClient struct {
	client         *armcostmanagement.QueryClient
	subscriptionID string
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
		client:         client,
		subscriptionID: cfg.SubscriptionID,
	}, nil
}

func (a *AzureCostClient) QueryCosts(ctx context.Context, start, end time.Time) ([]CURLineItem, error) {
	scope := fmt.Sprintf("/subscriptions/%s", a.subscriptionID)

	timeframe := armcostmanagement.TimeframeTypeCustom
	costType := armcostmanagement.ExportTypeActualCost
	granularity := armcostmanagement.GranularityTypeDaily

	startStr := start.Format("2006-01-02T00:00:00Z")
	// Azure Cost Management API 'to' is inclusive, adjust to exclusive boundary
	endStr := end.Add(-24 * time.Hour).Format("2006-01-02T00:00:00Z")

	query := armcostmanagement.QueryDefinition{
		Type:      &costType,
		Timeframe: &timeframe,
		TimePeriod: &armcostmanagement.QueryTimePeriod{
			From: parseAzureTime(startStr),
			To:   parseAzureTime(endStr),
		},
		Dataset: &armcostmanagement.QueryDataset{
			Granularity: &granularity,
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

	var result armcostmanagement.QueryClientUsageResponse
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		result, err = a.client.Usage(ctx, scope, query, nil)
		if err == nil {
			break
		}
		if strings.Contains(err.Error(), "429") && attempt < 2 {
			wait := time.Duration(60*(attempt+1)) * time.Second
			fmt.Printf("Rate limited, retrying in %v (%d/3)...\n", wait, attempt+2)
			time.Sleep(wait)
			continue
		}
		break
	}
	if err != nil {
		if strings.Contains(err.Error(), "NotFound") || strings.Contains(err.Error(), "offer type") {
			return nil, fmt.Errorf("azure Cost Management API not available — this can happen with free trial/sponsorship subscriptions, or if a recent subscription upgrade has not propagated yet (wait 1-2 hours): %w", err)
		}
		return nil, fmt.Errorf("azure cost query: %w", err)
	}

	return parseAzureCostResult(result), nil
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

		// Filter for VM-related resources
		isVM := strings.Contains(strings.ToLower(resourceType), "virtualmachine") ||
			strings.Contains(strings.ToLower(resourceID), "virtualmachine")
		if !isVM {
			continue
		}

		usageType := "BoxUsage"
		mcLower := strings.ToLower(meterCategory)
		if strings.Contains(mcLower, "network") || strings.Contains(mcLower, "bandwidth") {
			usageType = "DataTransfer"
		}

		pricingTerm := "OnDemand"
		if hasPricing && pricingCol < len(row) {
			if pm, _ := row[pricingCol].(string); pm != "" {
				switch strings.ToLower(pm) {
				case "reservation":
					pricingTerm = "Reserved"
				case "savingsplan":
					pricingTerm = "SavingsPlan"
				case "spot":
					pricingTerm = "Spot"
				}
			}
		}

		vmName := ParseProviderID(resourceID)

		items = append(items, CURLineItem{
			ResourceID:    vmName,
			EffectiveCost: cost,
			UsageType:     usageType,
			PricingTerm:   pricingTerm,
			InstanceType:  resourceType,
			Region:        "",
		})
	}

	return items
}

// ReconcileAzure runs the full Azure reconciliation flow.
func ReconcileAzure(ctx context.Context, client *AzureCostClient, nodes []collector.NodeInfo, estimatedCosts map[string]float64, namespaces []analyzer.NamespaceCost, start, end time.Time, periodDays float64) (*ReconciliationReport, error) {
	curItems, err := client.QueryCosts(ctx, start, end)
	if err != nil {
		return nil, err
	}
	curCosts := AggregateCURByResource(curItems)
	matched, unmatchedCUR, unmatchedNodes := MatchNodesToCUR(nodes, estimatedCosts, curCosts, periodDays)

	var totalEst, totalActual float64
	var riCount, spCount, spotCount, odCount int
	var riSavings, spSavings, spotSavings float64

	for _, n := range matched {
		totalEst += n.EstimatedMonthlyCost
		totalActual += n.ActualCost

		saving := n.EstimatedMonthlyCost - n.ActualCost
		if saving < 0 {
			saving = 0
		}

		switch n.PricingTerm {
		case "Reserved":
			riCount++
			riSavings += saving
		case "SavingsPlan":
			spCount++
			spSavings += saving
		case "Spot":
			spotCount++
			spotSavings += saving
		case "OnDemand":
			odCount++
		}
	}

	nsReconciliations := reconcileNamespaces(namespaces, totalEst, totalActual, nil, periodDays)

	totalDiff := totalActual - totalEst
	totalDiffPercent := 0.0
	if totalEst > 0 {
		totalDiffPercent = totalDiff / totalEst * 100
	}

	return &ReconciliationReport{
		GeneratedAt:        time.Now().UTC(),
		PeriodStart:        start,
		PeriodEnd:          end,
		DataDelay:          "Azure cost data delayed ~24-48h",
		TotalEstimatedCost: totalEst,
		TotalActualCost:    totalActual,
		TotalDifference:    totalDiff,
		TotalDiffPercent:   totalDiffPercent,
		Nodes:              matched,
		Namespaces:         nsReconciliations,
		RINodeCount:        riCount,
		SPNodeCount:        spCount,
		SpotNodeCount:      spotCount,
		OnDemandNodeCount:  odCount,
		TotalRISavings:     riSavings,
		TotalSPSavings:     spSavings,
		TotalSpotSavings:   spotSavings,
		UnmatchedCURItems:  unmatchedCUR,
		UnmatchedNodes:     unmatchedNodes,
	}, nil
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

func strPtr(s string) *string { return &s }
func funcPtr(f armcostmanagement.FunctionType) *armcostmanagement.FunctionType { return &f }
func columnPtr(c armcostmanagement.QueryColumnType) *armcostmanagement.QueryColumnType { return &c }
