package billing

import (
	"context"
	"fmt"
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
		info.Nodes, estimatedCosts, curCosts, periodDays, start,
	)

	// Query non-compute costs (disk, LB, IP, EKS fee)
	var warnings []string
	diskItems, lbItems, ipItems, eksCost, extraScanned, ncErr := r.athena.QueryNonComputeCosts(ctx, start, end)
	if ncErr != nil {
		slog.Warn("non-compute cost queries failed", "err", ncErr)
		warnings = append(warnings, fmt.Sprintf("non-compute cost queries partially failed: %v", ncErr))
	}
	queryResult.ScannedBytes += extraScanned

	// Match disks to PVCs
	pvEstimates := make(map[string]float64)
	for _, pv := range report.PVCosts {
		pvEstimates[pv.Namespace+"/"+pv.Name] = pv.MonthlyCost
	}
	var nodeNames []string
	for _, n := range info.Nodes {
		nodeNames = append(nodeNames, n.Name)
	}
	diskCosts := AggregateCURByResource(diskItems)
	matchedDisks, orphanedDisks := MatchDisksToPVCs(info.PVCs, pvEstimates, diskCosts, nodeNames, periodDays)

	// Match LBs to services
	lbEstimates := make(map[string]float64)
	for _, lb := range report.LBCosts {
		lbEstimates[lb.Namespace+"/"+lb.Name] = lb.MonthlyCost
	}
	lbCosts := AggregateCURByResource(lbItems)
	matchedLBs, orphanedLBs := MatchLBsToServices(info.LoadBalancers, lbEstimates, lbCosts, periodDays)

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

	// EKS management fee
	mgmtMonthly := 0.0
	if periodDays > 0 {
		mgmtMonthly = eksCost / periodDays * DaysPerMonth
	}

	var totalEst, totalActual float64
	var riCount, spCount, spotCount, odCount int
	var riSavings, spSavings, spotSavings float64

	for _, n := range nodes {
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

	var splitCosts map[string]float64
	if colSet.HasSplitLineItem {
		sc, err := r.athena.QuerySplitCostAllocation(ctx, start, end, colSet)
		if err != nil {
			slog.Warn("split cost allocation query failed, using proportional", "err", err)
		} else {
			splitCosts = sc
		}
	}

	nsReconciliations := reconcileNamespaces(report.Namespaces, totalEst, totalActual, splitCosts, periodDays)

	coverageGaps := DetectCoverageGaps(nodes)
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
		DataDelay:          "CUR data delayed ~48h",
		TotalEstimatedCost: infra.TotalEstimated,
		TotalActualCost:    infra.TotalActual,
		TotalDifference:    infraDiff,
		TotalDiffPercent:   infraDiffPct,
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
		Disks:              matchedDisks,
		OrphanedDisks:      orphanedDisks,
		LoadBalancers:      allLBs,
		PublicIPs:          publicIPs,
		CoverageGaps:       coverageGaps,
		InfraCost:          infra,
		Warnings:           warnings,
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
				actualCost = cost / periodDays * DaysPerMonth
				hasSplit = true
			}
		}

		computeCost := ns.MonthlyCost - ns.StorageCost

		if !hasSplit {
			ratio := 0.0
			if totalEstimated > 0 {
				ratio = computeCost / totalEstimated
			}
			actualCost = totalActual * ratio
		}

		diff := actualCost - computeCost
		diffPercent := 0.0
		if computeCost > 0 {
			diffPercent = diff / computeCost * 100
		}

		results = append(results, NamespaceReconciliation{
			Name:          ns.Name,
			EstimatedCost: computeCost,
			ActualCost:    actualCost,
			Difference:    diff,
			DiffPercent:   diffPercent,
			HasSplitCost:  hasSplit,
		})
	}
	return results
}
