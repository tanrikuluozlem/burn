package main

import (
	"strings"
	"testing"

	"github.com/tanrikuluozlem/burn/internal/billing"
)

func TestBuildReconcileVariance(t *testing.T) {
	r := &billing.ReconciliationReport{
		TotalDifference: 85.09,
		TotalDiffPercent: 26.0,
		InfraCost: &billing.InfrastructureSummary{
			ComputeEstimated: 307.84,
			ComputeActual:    308.92,
			DiskEstimated:    0,
			DiskActual:       9.35,
			LBEstimated:      19.71,
			LBActual:         19.83,
			UnmatchedCompute: 1.54,
			ManagementFee:    73.00,
		},
	}

	v := buildReconcileVariance(r)

	if !strings.Contains(v, "TOP-LEVEL VARIANCE") {
		t.Error("must contain top-level variance header")
	}
	if !strings.Contains(v, "+1.08") {
		t.Errorf("compute delta should be +1.08, got: %s", v)
	}
	if !strings.Contains(v, "+9.35") {
		t.Error("storage delta must be present")
	}
	if !strings.Contains(v, "+0.12") {
		t.Error("LB delta must be present")
	}
	if !strings.Contains(v, "+1.54") {
		t.Error("unreconciled must be present")
	}
	if !strings.Contains(v, "+73.00") {
		t.Error("management fee must be present")
	}
	if !strings.Contains(v, "+85.09") {
		t.Error("total difference must be present")
	}

	if strings.Contains(v, "transfer") || strings.Contains(v, "Transfer") {
		t.Error("must not contain data transfer as separate line")
	}
	if strings.Contains(v, "Spot") || strings.Contains(v, "Savings Plan") {
		t.Error("must not contain SP/Spot as separate variance line")
	}
}

func TestBuildReconcileVarianceNil(t *testing.T) {
	r := &billing.ReconciliationReport{}
	v := buildReconcileVariance(r)
	if v != "" {
		t.Errorf("expected empty for nil InfraCost, got: %s", v)
	}
}
