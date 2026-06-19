package billing

import (
	"context"
	"fmt"

	"github.com/tanrikuluozlem/burn/internal/analyzer"
)

type RISavingProvider interface {
	GetRISaving(ctx context.Context, instanceType, region string) (saving float64, pct float64, ok bool)
}

func BuildEstimateMaps(report *analyzer.CostReport) (estimatedCosts map[string]float64, pvEstimates map[string]float64, lbEstimates map[string]float64) {
	estimatedCosts = make(map[string]float64)
	for _, n := range report.Nodes {
		estimatedCosts[n.Name] = n.MonthlyPrice
	}
	pvEstimates = make(map[string]float64)
	for _, pv := range report.PVCosts {
		pvEstimates[pv.Namespace+"/"+pv.Name] = pv.MonthlyCost
	}
	lbEstimates = make(map[string]float64)
	for _, lb := range report.LBCosts {
		lbEstimates[lb.Namespace+"/"+lb.Name] = lb.MonthlyCost
	}
	return estimatedCosts, pvEstimates, lbEstimates
}

func EnrichCoverageGaps(ctx context.Context, gaps []CoverageGap, pp RISavingProvider) {
	for i := range gaps {
		gap := &gaps[i]
		saving, pct, ok := pp.GetRISaving(ctx, gap.InstanceType, gap.Region)
		if ok {
			gap.PotentialSaving = saving
			gap.Recommendation = fmt.Sprintf("$%.0f/mo (%.0f%% off) with 1yr Reserved Instance", saving, pct)
		}
	}
}
