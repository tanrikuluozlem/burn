package agent

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/tanrikuluozlem/burn/internal/plan"
	"github.com/tanrikuluozlem/burn/internal/pricing"
)

// Run against a real cluster with:
//
//	BURN_EKS_TEST=1 go test ./internal/agent/ -run TestEKS -v
//
// Optional: BURN_EKS_KUBECONFIG, BURN_EKS_CONTEXT, BURN_EKS_PROMETHEUS,
// BURN_EKS_PERIOD. Lists cluster objects only, and logs workload names and
// resource numbers but never kubeconfig contents, credentials, or pod specs.
func TestEKSWorkloadEvidence(t *testing.T) {
	if os.Getenv("BURN_EKS_TEST") != "1" {
		t.Skip("set BURN_EKS_TEST=1 to run against a real cluster")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	pp, err := pricing.NewCloudPricingProvider(ctx)
	if err != nil {
		t.Fatalf("pricing provider: %v", err)
	}

	tools := NewTools(Config{
		Kubeconfig:    os.Getenv("BURN_EKS_KUBECONFIG"),
		Kubecontext:   os.Getenv("BURN_EKS_CONTEXT"),
		PrometheusURL: os.Getenv("BURN_EKS_PROMETHEUS"),
		Period:        os.Getenv("BURN_EKS_PERIOD"),
	}, pp)

	records, err := tools.Analyze(ctx, AnalyzeInput{})
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("got %d evidence records, want 3", len(records))
	}

	var workloadRecord plan.Evidence
	for _, r := range records {
		t.Logf("evidence %s tool=%s bytes=%d", r.ID, r.Tool, len(r.Data))
		if r.ID != plan.EvidenceID(r.Tool, r.Data) {
			t.Errorf("evidence %s does not match its content", r.ID)
		}
		if r.Tool == toolWorkloads {
			workloadRecord = r
		}
	}

	got := decodeWorkloadEvidence(t, workloadRecord)
	t.Logf("metrics_source=%s period=%q workloads=%d unattributed=%d not_collected=%v",
		got.MetricsSource, got.PeriodEffective, len(got.Workloads), len(got.Unattributed), got.NotCollected)
	t.Logf("coverage %+v savings_semantics %+v", got.Coverage, got.SavingsSemantics)

	if got.Version != plan.WorkloadEvidenceVersion {
		t.Fatalf("evidence version = %d, want %d", got.Version, plan.WorkloadEvidenceVersion)
	}

	kinds := map[string]int{}
	pods, withRecommendation, blocked, priced := 0, 0, 0, 0

	for i, w := range got.Workloads {
		kinds[w.Kind]++
		pods += w.PodsAnalyzed
		withRecommendation += w.PodsWithRecommendation
		if len(w.Blockers) > 0 {
			blocked++
		}

		// The index is the path a plan cites, e.g. workloads.3.candidate_cpu_millicores.
		var curCPU, curMem int64
		if len(w.Pods) > 0 {
			curCPU, curMem = w.Pods[0].CPURequestMillicores, w.Pods[0].MemRequestBytes
		}
		// advisor is the per-pod sum burn analyze reports; candidate is the
		// figure for the single candidate.
		t.Logf("workloads[%d] %s/%s/%s pods=%d/%d/%d uniform=%t cpu %dm->%v mem %dB->%v advisor=%v candidate=%v blockers=%v",
			i, w.Namespace, w.Kind, w.Name,
			w.PodsWithRecommendation, w.PodsAnalyzed, w.PodsScheduled, w.CurrentRequestsUniform,
			curCPU, nullable(w.CandidateCPUMillicores),
			curMem, nullable(w.CandidateMemBytes),
			nullable(w.AdvisorPodSavingsMonthly),
			nullable(w.CandidateAllocationSavingsMonthly), w.Blockers)

		// The collector resolves a ReplicaSet up to its Deployment.
		if w.Kind == "ReplicaSet" {
			t.Errorf("workload %s/%s reported as ReplicaSet", w.Namespace, w.Name)
		}
		if w.PodsAnalyzed != len(w.Pods) {
			t.Errorf("workload %s/%s: pods_total=%d but %d pods listed", w.Namespace, w.Name, w.PodsAnalyzed, len(w.Pods))
		}
		if w.CandidateCPUMillicores == nil && w.CandidateMemBytes == nil && w.CandidateAllocationSavingsMonthly != nil {
			t.Errorf("workload %s/%s claims savings with no candidate", w.Namespace, w.Name)
		}

		// A published figure means every scheduled replica was priced.
		if w.CandidateAllocationSavingsMonthly != nil {
			priced++
			if w.PodsAnalyzed != w.PodsScheduled {
				t.Errorf("workload %s/%s priced %d of %d scheduled pods",
					w.Namespace, w.Name, w.PodsAnalyzed, w.PodsScheduled)
			}
			if w.CandidateCPUMillicores != nil && w.PodsWithCPURecommendation != w.PodsAnalyzed {
				t.Errorf("workload %s/%s priced cpu with %d of %d pods recommended",
					w.Namespace, w.Name, w.PodsWithCPURecommendation, w.PodsAnalyzed)
			}
			if w.CandidateMemBytes != nil && w.PodsWithMemRecommendation != w.PodsAnalyzed {
				t.Errorf("workload %s/%s priced memory with %d of %d pods recommended",
					w.Namespace, w.Name, w.PodsWithMemRecommendation, w.PodsAnalyzed)
			}
		}
		if w.CurrentRequestsUniform && w.PodsAnalyzed > 0 && w.CurrentCPUMillicores == nil {
			t.Errorf("workload %s/%s has uniform requests but no current value", w.Namespace, w.Name)
		}
	}

	t.Logf("kinds=%v pods_in_workloads=%d pods_with_recommendation=%d workloads_with_blockers=%d workloads_priced=%d",
		kinds, pods, withRecommendation, blocked, priced)

	for _, p := range got.Unattributed {
		t.Logf("unattributed %s/%s cpu=%dm mem=%dB", p.Namespace, p.Name, p.CPURequestMillicores, p.MemRequestBytes)
	}

	if got.MetricsSource == "requests" && withRecommendation > 0 {
		t.Error("recommendations produced without Prometheus metrics")
	}

	// A Prometheus URL that silently fails leaves requests-only evidence.
	if os.Getenv("BURN_EKS_PROMETHEUS") != "" {
		if got.MetricsSource != "prometheus" {
			t.Fatalf("metrics_source = %q with BURN_EKS_PROMETHEUS set; Prometheus was not reached", got.MetricsSource)
		}
		if got.PeriodRequested != got.PeriodEffective {
			t.Errorf("requested period %q but %q was used — Prometheus holds less history",
				got.PeriodRequested, got.PeriodEffective)
		}
		if withRecommendation == 0 {
			t.Error("Prometheus analysis produced no pod-level recommendation at all")
		}
	}

	t.Logf("compare with: kubectl get pods -A --no-headers | wc -l  (expect %d + unattributed %d)",
		pods, len(got.Unattributed))
}

// The evidence has to fit in a model context window.
func TestEKSEvidenceSize(t *testing.T) {
	if os.Getenv("BURN_EKS_TEST") != "1" {
		t.Skip("set BURN_EKS_TEST=1 to run against a real cluster")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	pp, err := pricing.NewCloudPricingProvider(ctx)
	if err != nil {
		t.Fatalf("pricing provider: %v", err)
	}

	tools := NewTools(Config{
		Kubeconfig:    os.Getenv("BURN_EKS_KUBECONFIG"),
		Kubecontext:   os.Getenv("BURN_EKS_CONTEXT"),
		PrometheusURL: os.Getenv("BURN_EKS_PROMETHEUS"),
		Period:        os.Getenv("BURN_EKS_PERIOD"),
	}, pp)

	records, err := tools.Analyze(ctx, AnalyzeInput{})
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}

	total := 0
	for _, r := range records {
		total += len(r.Data)
	}
	t.Logf("total evidence %d bytes across %d records", total, len(records))

	var sample map[string]json.RawMessage
	for _, r := range records {
		if r.Tool == toolWorkloads {
			if err := json.Unmarshal(r.Data, &sample); err != nil {
				t.Fatalf("workload evidence is not an object: %v", err)
			}
		}
	}
	if _, ok := sample["version"]; !ok {
		t.Error("workload evidence has no version field")
	}
}

func nullable(v *float64) any {
	if v == nil {
		return "null"
	}
	return *v
}
