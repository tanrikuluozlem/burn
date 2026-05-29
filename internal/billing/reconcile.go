package billing

import (
	"context"
	"log/slog"
	"math"
	"time"

	"github.com/tanrikuluozlem/burn/internal/analyzer"
	"github.com/tanrikuluozlem/burn/internal/collector"
)

type Reconciler struct {
	athena *AthenaClient
}

func NewReconciler(athena *AthenaClient) *Reconciler {
	return &Reconciler{athena: athena}
}

func (r *Reconciler) Reconcile(
	ctx context.Context,
	report *analyzer.CostReport,
	info *collector.ClusterInfo,
	start, end time.Time,
) (*ReconciliationReport, error) {
	colSet, err := r.athena.DetectColumns(ctx)
	if err != nil {
		return nil, err
	}

	var missing []string
	if !colSet.HasReservationARN {
		missing = append(missing, "reservation_reservation_a_r_n")
	}
	if !colSet.HasSavingsPlanARN {
		missing = append(missing, "savings_plan_savings_plan_a_r_n")
	}
	if !colSet.HasEffectiveCost {
		missing = append(missing, "reservation_effective_cost / savings_plan_effective_cost")
	}

	queryResult, err := r.athena.QueryCURForPeriod(ctx, start, end, colSet)
	if err != nil {
		return nil, err
	}

	curCosts := AggregateCURByResource(queryResult.Items)

	estimatedCosts := make(map[string]float64)
	for _, n := range report.Nodes {
		estimatedCosts[n.Name] = n.MonthlyPrice
	}

	periodDays := float64(queryResult.DaysQueried)
	if periodDays == 0 {
		periodDays = end.Sub(start).Hours() / 24
	}
	nodes, unmatchedCUR, unmatchedNodes := MatchNodesToCUR(
		info.Nodes, estimatedCosts, curCosts, periodDays,
	)

	var totalEst, totalActual float64
	var riCount, spCount, spotCount, odCount int
	var riSavings, spSavings, spotSavings float64

	for _, n := range nodes {
		if n.MatchMethod == "unmatched" {
			continue
		}
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

	var splitCosts map[string]float64
	if colSet.HasSplitLineItem {
		sc, err := r.athena.QuerySplitCostAllocation(ctx, start, end)
		if err != nil {
			slog.Warn("split cost allocation query failed, using proportional", "err", err)
		} else {
			splitCosts = sc
		}
	}

	nsReconciliations := reconcileNamespaces(report.Namespaces, totalEst, totalActual, splitCosts, periodDays)

	totalDiff := totalActual - totalEst
	totalDiffPercent := 0.0
	if totalEst > 0 {
		totalDiffPercent = totalDiff / totalEst * 100
	}

	return &ReconciliationReport{
		GeneratedAt:        time.Now().UTC(),
		PeriodStart:        start,
		PeriodEnd:          end,
		DataDelay:          "CUR data delayed ~48h",
		TotalEstimatedCost: totalEst,
		TotalActualCost:    totalActual,
		TotalDifference:    totalDiff,
		TotalDiffPercent:   totalDiffPercent,
		Nodes:              nodes,
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
		MissingCURColumns:  missing,
		DaysQueried:        queryResult.DaysQueried,
		DaysFailed:         queryResult.DaysFailed,
		DataScannedBytes:   queryResult.ScannedBytes,
	}, nil
}

func reconcileNamespaces(
	namespaces []analyzer.NamespaceCost,
	totalEstimated float64,
	totalActual float64,
	splitCosts map[string]float64,
	periodDays float64,
) []NamespaceReconciliation {
	var results []NamespaceReconciliation
	for _, ns := range namespaces {
		var actualCost float64
		hasSplit := false

		if splitCosts != nil && periodDays > 0 {
			if cost, ok := splitCosts[ns.Name]; ok {
				actualCost = cost / periodDays * 30.44
				hasSplit = true
			}
		}

		if !hasSplit {
			ratio := 0.0
			if totalEstimated > 0 {
				ratio = ns.MonthlyCost / totalEstimated
			}
			actualCost = totalActual * ratio
		}

		diff := actualCost - ns.MonthlyCost
		diffPercent := 0.0
		if ns.MonthlyCost > 0 {
			diffPercent = diff / ns.MonthlyCost * 100
		}

		results = append(results, NamespaceReconciliation{
			Name:          ns.Name,
			EstimatedCost: ns.MonthlyCost,
			ActualCost:    actualCost,
			Difference:    diff,
			DiffPercent:   diffPercent,
			HasSplitCost:  hasSplit,
		})
	}
	return results
}
