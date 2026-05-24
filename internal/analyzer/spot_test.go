package analyzer

import (
	"testing"

	"github.com/tanrikuluozlem/burn/internal/collector"
)

func TestCheckSpotReadiness(t *testing.T) {
	workloads := []collector.WorkloadInfo{
		{Name: "web", Namespace: "prod", Kind: "Deployment", Replicas: 3},
		{Name: "coredns", Namespace: "kube-system", Kind: "Deployment", Replicas: 2, PriorityClass: "system-cluster-critical"},
		{Name: "node-exporter", Namespace: "monitoring", Kind: "DaemonSet", Replicas: 4},
		{Name: "postgres", Namespace: "db", Kind: "StatefulSet", Replicas: 3},
		{Name: "worker", Namespace: "prod", Kind: "Deployment", Replicas: 1},
		{Name: "cache", Namespace: "prod", Kind: "Deployment", Replicas: 2, HasLocalStorage: true},
		{Name: "ml-inference", Namespace: "ml", Kind: "Deployment", Replicas: 2, HasGPU: true},
		{Name: "api", Namespace: "prod", Kind: "Deployment", Replicas: 2, PDBFound: true, PDBMinAvailable: 2},
		{Name: "api-maxunavail", Namespace: "prod", Kind: "Deployment", Replicas: 4, PDBFound: true, PDBMaxUnavailable: 1},
		{Name: "aggressive-deploy", Namespace: "prod", Kind: "Deployment", Replicas: 3, MaxUnavailable: 2},
		{Name: "safe-deploy", Namespace: "prod", Kind: "Deployment", Replicas: 3, MaxUnavailable: 0},
	}

	results := CheckSpotReadiness(workloads)

	expected := map[string]string{
		"web":               "spot-ready",
		"coredns":           "not-ready",
		"node-exporter":     "not-ready",
		"postgres":          "not-ready",
		"worker":            "not-ready",
		"cache":             "not-ready",
		"ml-inference":      "not-ready",
		"api":               "not-ready",
		"api-maxunavail":    "not-ready",
		"aggressive-deploy": "not-ready",
		"safe-deploy":       "spot-ready",
	}

	if len(results) != len(expected) {
		t.Fatalf("expected %d results, got %d", len(expected), len(results))
	}

	for _, r := range results {
		want, ok := expected[r.Name]
		if !ok {
			t.Errorf("unexpected workload %s", r.Name)
			continue
		}
		if r.Status != want {
			t.Errorf("%s: expected %s, got %s (reason: %s)", r.Name, want, r.Status, r.Reason)
		}
	}
}

func TestCheckSpotReadinessGPU(t *testing.T) {
	workloads := []collector.WorkloadInfo{
		{Name: "gpu-training", Kind: "Deployment", Replicas: 2, HasGPU: true},
		{Name: "gpu-inference", Kind: "Deployment", Replicas: 4, HasGPU: true},
		{Name: "cpu-only", Kind: "Deployment", Replicas: 2, HasGPU: false},
	}

	results := CheckSpotReadiness(workloads)

	for _, r := range results {
		switch r.Name {
		case "gpu-training", "gpu-inference":
			if r.Status != "not-ready" {
				t.Errorf("%s: GPU workload should be not-ready, got %s", r.Name, r.Status)
			}
		case "cpu-only":
			if r.Status != "spot-ready" {
				t.Errorf("%s: CPU-only should be spot-ready, got %s", r.Name, r.Status)
			}
		}
	}
}

func TestCheckSpotReadinessPDBMaxUnavailable(t *testing.T) {
	workloads := []collector.WorkloadInfo{
		{Name: "strict-pdb", Kind: "Deployment", Replicas: 4, PDBFound: true, PDBMaxUnavailable: 1},
		{Name: "loose-pdb", Kind: "Deployment", Replicas: 4, PDBFound: true, PDBMaxUnavailable: 3},
	}

	results := CheckSpotReadiness(workloads)

	for _, r := range results {
		switch r.Name {
		case "strict-pdb":
			// maxUnavailable=1 → minAvailable=3 → 3/4=75% > 50% → not-ready
			if r.Status != "not-ready" {
				t.Errorf("strict-pdb: expected not-ready, got %s (reason: %s)", r.Status, r.Reason)
			}
		case "loose-pdb":
			// maxUnavailable=3 → minAvailable=1 → 1/4=25% < 50% → spot-ready
			if r.Status != "spot-ready" {
				t.Errorf("loose-pdb: expected spot-ready, got %s (reason: %s)", r.Status, r.Reason)
			}
		}
	}
}

func TestSpotSavings(t *testing.T) {
	results := []SpotReadiness{
		{Status: "spot-ready", MonthlyCost: 100, Discount: 0.5},
		{Status: "spot-ready", MonthlyCost: 50, Discount: 0.5},
		{Status: "not-ready", MonthlyCost: 200, Discount: 0},
	}

	savings := SpotSavings(results)
	if !floatEquals(savings, 75.0) {
		t.Errorf("expected 75.0, got %.2f", savings)
	}
}

func TestSpotSavingsNoDiscount(t *testing.T) {
	results := []SpotReadiness{
		{Status: "spot-ready", MonthlyCost: 100, Discount: 0},
	}

	savings := SpotSavings(results)
	if savings != 0 {
		t.Errorf("expected 0 when no discount, got %.2f", savings)
	}
}
