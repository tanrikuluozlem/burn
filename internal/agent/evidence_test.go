package agent

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/tanrikuluozlem/burn/internal/advisor"
	"github.com/tanrikuluozlem/burn/internal/analyzer"
	"github.com/tanrikuluozlem/burn/internal/collector"
	"github.com/tanrikuluozlem/burn/internal/plan"
)

// overProvisionedPod clears the savings engine's $1 threshold on both
// resources.
func overProvisionedPod(namespace, name, ownerKind, ownerName string) analyzer.PodEfficiency {
	return analyzer.PodEfficiency{
		Name:            name,
		Namespace:       namespace,
		OwnerKind:       ownerKind,
		OwnerName:       ownerName,
		CPURequest:      2000,
		CPUUsage:        0.3,
		CPUEfficiency:   0.15,
		CPUCost:         40,
		CPUP95Usage:     0.3,
		CPUP95Available: true,
		MemRequest:      4 << 30,
		MemUsage:        1 << 30,
		MemEfficiency:   0.25,
		RAMCost:         20,
		MemoryP95Usage:  1 << 30,
		MemP95Available: true,
		MonthlyCost:     60,
	}
}

func workloadReport(pods ...analyzer.PodEfficiency) *analyzer.CostReport {
	return &analyzer.CostReport{
		MetricsSource: "prometheus",
		Period:        "7d",
		MonthlyCost:   300,
		AllPods:       pods,
	}
}

func decodeWorkloadEvidence(t *testing.T, e plan.Evidence) workloadEvidence {
	t.Helper()
	var got workloadEvidence
	if err := json.Unmarshal(e.Data, &got); err != nil {
		t.Fatalf("unmarshal workload evidence: %v", err)
	}
	return got
}

func TestWorkloadEvidenceGroupsPodsByController(t *testing.T) {
	report := workloadReport(
		overProvisionedPod("prod", "api-7d9f-aaa", "Deployment", "api"),
		overProvisionedPod("prod", "api-7d9f-bbb", "Deployment", "api"),
		overProvisionedPod("db", "pg-0", "StatefulSet", "pg"),
	)

	got := decodeWorkloadEvidence(t, mustBuild(t, report))

	if len(got.Workloads) != 2 {
		t.Fatalf("got %d workloads, want 2: %+v", len(got.Workloads), got.Workloads)
	}
	// Sorted by namespace/kind/name.
	if got.Workloads[0].Kind != "StatefulSet" || got.Workloads[0].Name != "pg" {
		t.Errorf("first workload = %+v, want StatefulSet/pg", got.Workloads[0])
	}
	if got.Workloads[1].Kind != "Deployment" || len(got.Workloads[1].Pods) != 2 {
		t.Errorf("second workload = %+v, want Deployment with 2 pods", got.Workloads[1])
	}
	if len(got.Unattributed) != 0 {
		t.Errorf("unexpected unattributed pods: %+v", got.Unattributed)
	}
}

// The collector leaves the owner empty when a ReplicaSet does not resolve.
func TestWorkloadEvidenceKeepsUnresolvedOwnersSeparate(t *testing.T) {
	report := workloadReport(
		overProvisionedPod("prod", "api-7d9f-aaa", "Deployment", "api"),
		overProvisionedPod("prod", "orphan", "", ""),
	)

	got := decodeWorkloadEvidence(t, mustBuild(t, report))

	if len(got.Workloads) != 1 {
		t.Errorf("got %d workloads, want 1", len(got.Workloads))
	}
	if len(got.Unattributed) != 1 || got.Unattributed[0].Name != "orphan" {
		t.Errorf("unattributed = %+v, want the orphan pod", got.Unattributed)
	}
}

func TestWorkloadEvidenceCoversEveryPodNotJustTopTen(t *testing.T) {
	var pods []analyzer.PodEfficiency
	for i := range 25 {
		pods = append(pods, overProvisionedPod("prod", "api-"+string(rune('a'+i)), "Deployment", "api"))
	}
	report := workloadReport(pods...)

	got := decodeWorkloadEvidence(t, mustBuild(t, report))

	if len(got.Workloads) != 1 || len(got.Workloads[0].Pods) != 25 {
		t.Errorf("got %d pods, want all 25", len(got.Workloads[0].Pods))
	}
}

func TestWorkloadEvidenceLeavesUnmeasuredValuesNull(t *testing.T) {
	pod := overProvisionedPod("prod", "api-1", "Deployment", "api")
	pod.CPUP95Available = false
	pod.CPUP95Usage = 0
	pod.MemP95Available = false
	pod.MemoryP95Usage = 0

	e := mustBuild(t, workloadReport(pod))
	got := decodeWorkloadEvidence(t, e)

	candidate := got.Workloads[0].Pods[0]
	if candidate.CPUP95Cores != nil || candidate.MemP95Bytes != nil {
		t.Errorf("unmeasured p95 should be null, got %+v", candidate)
	}
	if candidate.RecommendedCPUMillicores != nil || candidate.RecommendedMemBytes != nil {
		t.Errorf("no p95 means no recommendation, got %+v", candidate)
	}

	// null, not 0 — a plan must not cite an unmeasured value.
	for _, field := range []string{`"cpu_p95_cores":null`, `"recommended_cpu_millicores":null`} {
		if !strings.Contains(string(e.Data), field) {
			t.Errorf("evidence should contain %s, got %s", field, e.Data)
		}
	}
}

// Without Prometheus the engine falls back to node-level right-sizing.
func TestWorkloadEvidenceHasNoRecommendationsWithoutPrometheus(t *testing.T) {
	report := workloadReport(overProvisionedPod("prod", "api-1", "Deployment", "api"))
	report.MetricsSource = "requests"
	report.Period = ""

	got := decodeWorkloadEvidence(t, mustBuild(t, report))

	candidate := got.Workloads[0].Pods[0]
	if candidate.RecommendedCPUMillicores != nil || candidate.RecommendedMemBytes != nil {
		t.Errorf("request-only metrics must not yield recommendations, got %+v", candidate)
	}
	if got.MetricsSource != "requests" {
		t.Errorf("metrics_source = %q, want %q", got.MetricsSource, "requests")
	}
}

// Content-addressed ids: pod order must not change the bytes.
func TestWorkloadEvidenceIsDeterministic(t *testing.T) {
	a := overProvisionedPod("prod", "api-a", "Deployment", "api")
	b := overProvisionedPod("prod", "api-b", "Deployment", "api")
	c := overProvisionedPod("db", "pg-0", "StatefulSet", "pg")

	first := mustBuild(t, workloadReport(a, b, c))
	shuffled := mustBuild(t, workloadReport(c, b, a))

	if first.ID != shuffled.ID {
		t.Errorf("pod order changed the evidence id: %s then %s", first.ID, shuffled.ID)
	}
}

func TestWorkloadEvidenceFieldNamesAreStable(t *testing.T) {
	e := mustBuild(t, workloadReport(overProvisionedPod("prod", "api-1", "Deployment", "api")))

	for _, field := range []string{
		`"version":3`,
		`"metrics_source":`, `"workloads":`,
		`"kind":`, `"namespace":`, `"name":`, `"pods":`,
		`"cpu_request_millicores":`, `"mem_request_bytes":`,
		`"cpu_usage_cores":`, `"mem_usage_bytes":`,
		`"cpu_p95_cores":`, `"mem_p95_bytes":`,
		`"recommended_cpu_millicores":`, `"recommended_mem_bytes":`,
		`"not_collected":`, `"pods_analyzed":`, `"pods_scheduled":`, `"excluded_other":`,
		`"inventory_unavailable":`,
		`"period_requested":`, `"period_effective":`, `"pods_with_recommendation":`,
		`"current_requests_uniform":`, `"candidate_cpu_millicores":`,
		`"candidate_mem_bytes":`, `"candidate_allocation_savings_monthly":`,
		`"cpu_cost_monthly":`, `"mem_cost_monthly":`,
		`"current_cpu_millicores":`, `"current_mem_bytes":`,
		`"pods_with_cpu_recommendation":`, `"pods_with_mem_recommendation":`,
		`"advisor_pod_savings_monthly":`,
		`"candidate_cpu_savings_monthly":`, `"candidate_mem_savings_monthly":`,
	} {
		if !strings.Contains(string(e.Data), field) {
			t.Errorf("missing stable field %s in %s", field, e.Data)
		}
	}
}

func TestWorkloadEvidenceSupportsAValidPlan(t *testing.T) {
	report := workloadReport(overProvisionedPod("prod", "api-1", "Deployment", "api"))
	workloads := mustBuild(t, report)

	decoded := decodeWorkloadEvidence(t, workloads)
	w := decoded.Workloads[0]
	if w.CandidateCPUMillicores == nil || w.CandidateAllocationSavingsMonthly == nil {
		t.Fatalf("fixture produced no priced candidate: %+v", w)
	}

	ref := func(path string) plan.EvidenceRef {
		return plan.EvidenceRef{EvidenceID: workloads.ID, Path: path}
	}
	p := &plan.Plan{
		SchemaVersion: plan.SchemaVersion,
		ID:            "run-1",
		CreatedAt:     time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC),
		Goal:          "reduce cost without reliability risk",
		Readiness:     plan.ReadinessInvestigation,
		Target:        plan.Target{Kind: "Deployment", Namespace: "prod", Name: "api"},
		Current: plan.Resources{
			CPUMillicores: &plan.Value{Amount: *w.CurrentCPUMillicores, Source: ref("workloads.0.current_cpu_millicores")},
			MemoryBytes:   &plan.Value{Amount: *w.CurrentMemBytes, Source: ref("workloads.0.current_mem_bytes")},
		},
		Proposed: plan.Resources{
			CPUMillicores: &plan.Value{Amount: *w.CandidateCPUMillicores, Source: ref("workloads.0.candidate_cpu_millicores")},
			MemoryBytes:   &plan.Value{Amount: *w.CandidateMemBytes, Source: ref("workloads.0.candidate_mem_bytes")},
		},
		ExpectedSavings: &plan.Value{Amount: *w.CandidateAllocationSavingsMonthly,
			Source: ref("workloads.0.candidate_allocation_savings_monthly")},
		Confidence:               "medium",
		Blockers:                 w.Blockers,
		UnsupportedPrerequisites: decoded.NotCollected,
	}

	if err := plan.Validate(p, []plan.Evidence{workloads}); err != nil {
		t.Fatalf("plan built from real evidence was rejected: %v", err)
	}
}

func TestClusterSavingsCannotBackAWorkloadPlan(t *testing.T) {
	report := workloadReport(overProvisionedPod("prod", "api-1", "Deployment", "api"))
	workloads := mustBuild(t, report)
	savings, err := savingsEvidence(advisor.CalculateSavings(report, advisor.DefaultSavingsConfig()))
	if err != nil {
		t.Fatalf("savingsEvidence() error = %v", err)
	}

	decoded := decodeWorkloadEvidence(t, workloads)
	w := decoded.Workloads[0]

	p := &plan.Plan{
		SchemaVersion: plan.SchemaVersion,
		ID:            "run-1",
		Goal:          "reduce cost",
		Readiness:     plan.ReadinessInvestigation,
		Target:        plan.Target{Kind: "Deployment", Namespace: "prod", Name: "api"},
		Current: plan.Resources{
			CPUMillicores: &plan.Value{Amount: *w.CurrentCPUMillicores, Source: plan.EvidenceRef{
				EvidenceID: workloads.ID, Path: "workloads.0.current_cpu_millicores"}},
		},
		ExpectedSavings: &plan.Value{Amount: 31, Source: plan.EvidenceRef{
			EvidenceID: savings.ID, Path: "right_sizing"}},
		Confidence:               "medium",
		Blockers:                 w.Blockers,
		UnsupportedPrerequisites: decoded.NotCollected,
	}

	if err := plan.Validate(p, []plan.Evidence{workloads, savings}); err == nil {
		t.Fatal("cluster-wide savings were accepted for a single workload")
	}
}

// mustBuild derives a ClusterInfo where every analyzed pod was also
// discovered.
func mustBuild(t *testing.T, report *analyzer.CostReport) plan.Evidence {
	t.Helper()
	return mustBuildWith(t, report, infoFor(report.AllPods...))
}

func mustBuildWith(t *testing.T, report *analyzer.CostReport, info *collector.ClusterInfo) plan.Evidence {
	t.Helper()
	e, err := buildWorkloadEvidence(report, info, advisor.CalculateSavings(report, advisor.DefaultSavingsConfig()), report.Period)
	if err != nil {
		t.Fatalf("buildWorkloadEvidence() error = %v", err)
	}
	return e
}

func infoFor(pods ...analyzer.PodEfficiency) *collector.ClusterInfo {
	node := collector.NodeInfo{Name: "node-a"}
	for _, p := range pods {
		node.Pods = append(node.Pods, collector.PodInfo{
			Name: p.Name, Namespace: p.Namespace,
			OwnerKind: p.OwnerKind, OwnerName: p.OwnerName,
			CPURequest: p.CPURequest, MemoryRequest: p.MemRequest,
		})
	}
	return &collector.ClusterInfo{Nodes: []collector.NodeInfo{node}, TotalPods: len(pods)}
}

// The smallest recommendation would set the busiest pod below its own.
func TestWorkloadCandidateTakesTheLargestRecommendation(t *testing.T) {
	busy := overProvisionedPod("prod", "api-busy", "Deployment", "api")
	busy.CPUP95Usage = 0.4
	quiet := overProvisionedPod("prod", "api-quiet", "Deployment", "api")
	quiet.CPUP95Usage = 0.1

	report := workloadReport(busy, quiet)
	w := decodeWorkloadEvidence(t, mustBuild(t, report)).Workloads[0]

	if w.CandidateCPUMillicores == nil {
		t.Fatalf("no candidate produced: %+v", w)
	}

	var want float64
	for _, p := range advisor.CalculateSavings(report, advisor.DefaultSavingsConfig()).RightSizing.Pods {
		if p.RecommendedCPUMillicores > want {
			want = p.RecommendedCPUMillicores
		}
	}
	if *w.CandidateCPUMillicores != want {
		t.Errorf("candidate cpu = %v, want %v (the busier pod)", *w.CandidateCPUMillicores, want)
	}
	if quiet.CPUP95Usage >= busy.CPUP95Usage {
		t.Fatal("fixture does not distinguish the busier pod")
	}
	if w.PodsWithRecommendation != 2 || w.PodsAnalyzed != 2 {
		t.Errorf("coverage = %d/%d, want 2/2", w.PodsWithRecommendation, w.PodsAnalyzed)
	}
	if len(w.Blockers) != 0 {
		t.Errorf("fully covered uniform workload should have no blockers, got %v", w.Blockers)
	}
}

func TestWorkloadBlocksPartialCoverage(t *testing.T) {
	measured := overProvisionedPod("prod", "api-a", "Deployment", "api")
	unmeasured := overProvisionedPod("prod", "api-b", "Deployment", "api")
	unmeasured.CPUP95Available = false
	unmeasured.MemP95Available = false

	w := decodeWorkloadEvidence(t, mustBuild(t, workloadReport(measured, unmeasured))).Workloads[0]

	if w.PodsWithRecommendation != 1 || w.PodsAnalyzed != 2 {
		t.Fatalf("coverage = %d/%d, want 1/2", w.PodsWithRecommendation, w.PodsAnalyzed)
	}
	if !containsSubstring(w.Blockers, "covers 1 of 2 analyzed pods") {
		t.Errorf("blockers = %v, want partial coverage recorded", w.Blockers)
	}
}

func TestWorkloadBlocksInconsistentRequests(t *testing.T) {
	a := overProvisionedPod("prod", "api-a", "Deployment", "api")
	b := overProvisionedPod("prod", "api-b", "Deployment", "api")
	b.CPURequest = 4000

	w := decodeWorkloadEvidence(t, mustBuild(t, workloadReport(a, b))).Workloads[0]

	if w.CurrentRequestsUniform {
		t.Error("pods with different requests reported as uniform")
	}
	if !containsSubstring(w.Blockers, "same requests") {
		t.Errorf("blockers = %v, want the request mismatch recorded", w.Blockers)
	}
}

func TestWorkloadWithoutRecommendationIsBlockedAndNull(t *testing.T) {
	pod := overProvisionedPod("prod", "api-a", "Deployment", "api")
	pod.CPUP95Available = false
	pod.MemP95Available = false

	w := decodeWorkloadEvidence(t, mustBuild(t, workloadReport(pod))).Workloads[0]

	if w.CandidateCPUMillicores != nil || w.CandidateMemBytes != nil || w.CandidateAllocationSavingsMonthly != nil {
		t.Errorf("no recommendation must leave candidates null, got %+v", w)
	}
	if w.CandidateCPUSavingsMonthly != nil || w.CandidateMemSavingsMonthly != nil {
		t.Errorf("no candidate means no per-resource savings, got %+v", w)
	}
	if !containsSubstring(w.Blockers, "no pod-level recommendation") {
		t.Errorf("blockers = %v, want the missing recommendation recorded", w.Blockers)
	}
}

func TestAdvisorPodSavingsSumPodSavings(t *testing.T) {
	report := workloadReport(
		overProvisionedPod("prod", "api-a", "Deployment", "api"),
		overProvisionedPod("prod", "api-b", "Deployment", "api"),
	)
	savings := advisor.CalculateSavings(report, advisor.DefaultSavingsConfig())

	var want float64
	for _, p := range savings.RightSizing.Pods {
		want += p.CPUSavings + p.MemSavings
	}

	w := decodeWorkloadEvidence(t, mustBuild(t, report)).Workloads[0]
	if w.AdvisorPodSavingsMonthly == nil || *w.AdvisorPodSavingsMonthly != want {
		t.Errorf("advisor pod savings = %v, want %v", w.AdvisorPodSavingsMonthly, want)
	}
}

func TestEvidenceDeclaresWhatItDoesNotCollect(t *testing.T) {
	got := decodeWorkloadEvidence(t, mustBuild(t, workloadReport(
		overProvisionedPod("prod", "api-a", "Deployment", "api"))))

	for _, want := range []string{"hpa", "rollout_strategy", "container_level_requests"} {
		if !containsSubstring(got.NotCollected, want) {
			t.Errorf("not_collected = %v, want it to name %q", got.NotCollected, want)
		}
	}
}

func containsSubstring(values []string, want string) bool {
	for _, v := range values {
		if strings.Contains(v, want) {
			return true
		}
	}
	return false
}

func TestCoverageCountsExcludedPods(t *testing.T) {
	analyzed := overProvisionedPod("prod", "api-a", "Deployment", "api")
	zeroRequest := collector.PodInfo{Name: "sidecar", Namespace: "prod", OwnerKind: "Deployment", OwnerName: "api"}

	info := infoFor(analyzed)
	info.Nodes[0].Pods = append(info.Nodes[0].Pods, zeroRequest)
	info.TotalPods = 3 // one more pod was discovered but never scheduled

	got := decodeWorkloadEvidence(t, mustBuildWith(t, workloadReport(analyzed), info))

	want := podCoverage{
		PodsDiscovered: 3, PodsAnalyzed: 1, PodsExcluded: 2,
		PodsAttributed: 1, PodsUnattributed: 0,
		ExcludedUnscheduled: 1, ExcludedNoRequests: 1, ExcludedOther: 0,
	}
	if got.Coverage != want {
		t.Errorf("coverage = %+v, want %+v", got.Coverage, want)
	}
}

func TestCoverageAccountsForUnobservedExclusions(t *testing.T) {
	analyzed := overProvisionedPod("prod", "api-a", "Deployment", "api")

	info := infoFor(analyzed)
	info.Nodes[0].Pods = append(info.Nodes[0].Pods,
		collector.PodInfo{Name: "dropped", Namespace: "prod", OwnerKind: "Deployment", OwnerName: "api", CPURequest: 100})
	info.TotalPods = 2

	c := decodeWorkloadEvidence(t, mustBuildWith(t, workloadReport(analyzed), info)).Coverage

	if c.ExcludedOther != 1 {
		t.Errorf("excluded_other = %d, want 1 (a pod with requests the analyzer still dropped)", c.ExcludedOther)
	}
	assertCoverageInvariants(t, c)
}

// Attribution always holds. Discovery and exclusion counts only mean anything
// with a cluster view, and must stay zero without one.
func assertCoverageInvariants(t *testing.T, c podCoverage) {
	t.Helper()
	if c.PodsAttributed+c.PodsUnattributed != c.PodsAnalyzed {
		t.Errorf("attributed %d + unattributed %d != analyzed %d", c.PodsAttributed, c.PodsUnattributed, c.PodsAnalyzed)
	}

	if c.InventoryUnavailable {
		if c.PodsDiscovered != 0 || c.PodsExcluded != 0 || c.ExcludedUnscheduled != 0 ||
			c.ExcludedNoRequests != 0 || c.ExcludedOther != 0 {
			t.Errorf("counts must not be inferred without a cluster view: %+v", c)
		}
		return
	}

	if sum := c.ExcludedUnscheduled + c.ExcludedNoRequests + c.ExcludedOther; sum != c.PodsExcluded {
		t.Errorf("exclusion categories sum to %d, want pods_excluded %d", sum, c.PodsExcluded)
	}
	if c.PodsAnalyzed+c.PodsExcluded != c.PodsDiscovered {
		t.Errorf("analyzed %d + excluded %d != discovered %d", c.PodsAnalyzed, c.PodsExcluded, c.PodsDiscovered)
	}
}

func TestWorkloadWithExcludedPodsIsBlocked(t *testing.T) {
	analyzed := overProvisionedPod("prod", "api-a", "Deployment", "api")

	info := infoFor(analyzed)
	info.Nodes[0].Pods = append(info.Nodes[0].Pods,
		collector.PodInfo{Name: "api-b", Namespace: "prod", OwnerKind: "Deployment", OwnerName: "api"})
	info.TotalPods = 2

	w := decodeWorkloadEvidence(t, mustBuildWith(t, workloadReport(analyzed), info)).Workloads[0]

	if w.PodsScheduled != 2 || w.PodsAnalyzed != 1 {
		t.Fatalf("discovered/analyzed = %d/%d, want 2/1", w.PodsScheduled, w.PodsAnalyzed)
	}
	if !containsSubstring(w.Blockers, "1 of 2 scheduled pods excluded") {
		t.Errorf("blockers = %v, want the exclusion recorded", w.Blockers)
	}
}

func TestWorkloadWithNoAnalyzablePodsIsListed(t *testing.T) {
	info := &collector.ClusterInfo{
		Workloads: []collector.WorkloadInfo{{Name: "batch", Namespace: "jobs", Kind: "Deployment"}},
	}

	got := decodeWorkloadEvidence(t, mustBuildWith(t, workloadReport(), info))

	if len(got.Workloads) != 1 {
		t.Fatalf("got %d workloads, want the unanalyzed one listed", len(got.Workloads))
	}
	w := got.Workloads[0]
	if w.Name != "batch" || w.PodsAnalyzed != 0 {
		t.Errorf("workload = %+v, want batch with no analyzed pods", w)
	}
	if !containsSubstring(w.Blockers, "no analyzable pods") {
		t.Errorf("blockers = %v, want the empty analysis recorded", w.Blockers)
	}
	if w.CandidateAllocationSavingsMonthly != nil {
		t.Error("an unanalyzed workload must not carry a savings figure")
	}
}

func TestPodRecordsWhyItHasNoRecommendation(t *testing.T) {
	noP95 := overProvisionedPod("prod", "a", "Deployment", "api")
	noP95.CPUP95Available, noP95.MemP95Available = false, false

	noCost := overProvisionedPod("prod", "b", "Deployment", "api")
	noCost.CPUCost, noCost.RAMCost = 0, 0

	got := decodeWorkloadEvidence(t, mustBuild(t, workloadReport(noP95, noCost)))
	pods := got.Workloads[0].Pods

	if pods[0].CPUNoRecommendation != "p95_missing" || pods[0].MemNoRecommendation != "p95_missing" {
		t.Errorf("reasons = %q/%q, want p95_missing on both", pods[0].CPUNoRecommendation, pods[0].MemNoRecommendation)
	}
	if pods[1].CPUNoRecommendation != "no_cost" || pods[1].MemNoRecommendation != "no_cost" {
		t.Errorf("reasons = %q/%q, want no_cost on both", pods[1].CPUNoRecommendation, pods[1].MemNoRecommendation)
	}
}

func TestDeclineReasonIsPerResource(t *testing.T) {
	pod := overProvisionedPod("prod", "a", "Deployment", "api")
	pod.MemP95Available = false

	got := decodeWorkloadEvidence(t, mustBuild(t, workloadReport(pod))).Workloads[0].Pods[0]

	if got.RecommendedCPUMillicores == nil {
		t.Fatal("CPU should still be recommended")
	}
	if got.CPUNoRecommendation != "" {
		t.Errorf("cpu reason = %q, want none when CPU was recommended", got.CPUNoRecommendation)
	}
	if got.MemNoRecommendation != "p95_missing" {
		t.Errorf("mem reason = %q, want p95_missing", got.MemNoRecommendation)
	}
}

// Prometheus may hold less history than requested.
func TestPeriodsAreReportedSeparately(t *testing.T) {
	report := workloadReport(overProvisionedPod("prod", "api-1", "Deployment", "api"))
	report.Period = "3d"

	e, err := buildWorkloadEvidence(report, infoFor(report.AllPods...),
		advisor.CalculateSavings(report, advisor.DefaultSavingsConfig()), "7d")
	if err != nil {
		t.Fatalf("buildWorkloadEvidence() error = %v", err)
	}

	got := decodeWorkloadEvidence(t, e)
	if got.PeriodRequested != "7d" || got.PeriodEffective != "3d" {
		t.Errorf("requested/effective = %q/%q, want 7d/3d", got.PeriodRequested, got.PeriodEffective)
	}
}

// Allocation savings must not read as verified against a bill.
func TestSavingsSemanticsAreDeclared(t *testing.T) {
	e := mustBuild(t, workloadReport(overProvisionedPod("prod", "api-1", "Deployment", "api")))
	got := decodeWorkloadEvidence(t, e)

	if got.SavingsSemantics.Basis != "allocation" {
		t.Errorf("basis = %q, want allocation", got.SavingsSemantics.Basis)
	}
	if got.SavingsSemantics.Stage != "identified" {
		t.Errorf("stage = %q, want identified", got.SavingsSemantics.Stage)
	}
	if got.SavingsSemantics.FinancialVerification != "not_performed" {
		t.Errorf("financial_verification = %q, want not_performed", got.SavingsSemantics.FinancialVerification)
	}
	for _, factor := range []string{
		"infrastructure_capacity_reduction",
		"avoided_capacity_growth",
		"commitment_coverage_and_utilization",
	} {
		if !containsSubstring(got.SavingsSemantics.RealizationFactors, factor) {
			t.Errorf("realization_factors = %v, want it to name %q", got.SavingsSemantics.RealizationFactors, factor)
		}
	}
	if !strings.Contains(string(e.Data), `"savings_semantics"`) {
		t.Error("savings_semantics missing from the serialized evidence")
	}
}

func advisorPodSum(t *testing.T, report *analyzer.CostReport) float64 {
	t.Helper()
	var total float64
	for _, p := range advisor.CalculateSavings(report, advisor.DefaultSavingsConfig()).RightSizing.Pods {
		total += p.CPUSavings + p.MemSavings
	}
	return total
}

func closeTo(got, want float64) bool {
	return math.Abs(got-want) <= 1e-9*math.Max(1, math.Abs(want))
}

// One pod means the candidate is that pod's own recommendation, so the figures
// must agree exactly.
func TestCandidateSavingsMatchAdvisorForASinglePod(t *testing.T) {
	report := workloadReport(overProvisionedPod("prod", "api-1", "Deployment", "api"))
	want := advisorPodSum(t, report)

	w := decodeWorkloadEvidence(t, mustBuild(t, report)).Workloads[0]
	if w.CandidateAllocationSavingsMonthly == nil {
		t.Fatalf("no candidate savings produced: %+v", w)
	}
	if !closeTo(*w.CandidateAllocationSavingsMonthly, want) {
		t.Errorf("candidate savings = %v, want the advisor's %v",
			*w.CandidateAllocationSavingsMonthly, want)
	}
}

// A workload holds one request for every replica, so per-pod sums describe a
// change that cannot be applied.
func TestCandidateSavingsPriceTheCandidateNotEachPod(t *testing.T) {
	quiet := overProvisionedPod("prod", "api-quiet", "Deployment", "api")
	quiet.CPUP95Usage, quiet.CPUCost, quiet.MemP95Available = 0.1, 40, false

	busy := overProvisionedPod("prod", "api-busy", "Deployment", "api")
	busy.CPUP95Usage, busy.CPUCost, busy.MemP95Available = 0.4, 8, false

	report := workloadReport(quiet, busy)
	w := decodeWorkloadEvidence(t, mustBuild(t, report)).Workloads[0]

	// Candidate 600m: 40*(2000-600)/2000 + 8*(2000-600)/2000.
	const wantCandidate = 28.0 + 5.6
	if w.CandidateCPUSavingsMonthly == nil || !closeTo(*w.CandidateCPUSavingsMonthly, wantCandidate) {
		t.Errorf("candidate cpu savings = %v, want %v", w.CandidateCPUSavingsMonthly, wantCandidate)
	}

	// 40*(1-150/2000) + 8*(1-600/2000), each pod at its own recommendation.
	wantAdvisor := advisorPodSum(t, report)
	if w.AdvisorPodSavingsMonthly == nil || !closeTo(*w.AdvisorPodSavingsMonthly, wantAdvisor) {
		t.Fatalf("advisor savings = %v, want %v", w.AdvisorPodSavingsMonthly, wantAdvisor)
	}
	if *w.CandidateAllocationSavingsMonthly >= *w.AdvisorPodSavingsMonthly {
		t.Errorf("candidate savings %v should be below the per-pod sum %v",
			*w.CandidateAllocationSavingsMonthly, *w.AdvisorPodSavingsMonthly)
	}
}

func TestCandidateSavingsGoNegativeWhenTheCandidateRaisesAPod(t *testing.T) {
	big := overProvisionedPod("prod", "api-big", "Deployment", "api")
	big.CPURequest, big.CPUCost, big.CPUP95Usage, big.MemP95Available = 2000, 40, 1.2, false

	small := overProvisionedPod("prod", "api-small", "Deployment", "api")
	small.CPURequest, small.CPUCost, small.CPUP95Usage, small.MemP95Available = 500, 10, 0.1, false

	report := workloadReport(big, small)
	w := decodeWorkloadEvidence(t, mustBuild(t, report)).Workloads[0]

	// Candidate 1800m: 40*(2000-1800)/2000 + 10*(500-1800)/500.
	const want = 4.0 - 26.0
	if w.CandidateCPUSavingsMonthly == nil || !closeTo(*w.CandidateCPUSavingsMonthly, want) {
		t.Fatalf("candidate cpu savings = %v, want %v", w.CandidateCPUSavingsMonthly, want)
	}
	if sum := advisorPodSum(t, report); sum <= 0 {
		t.Fatalf("fixture should show the sign flip: per-pod sum = %v", sum)
	}
	if !containsSubstring(w.Blockers, "cpu candidate is above the current request of 1 of 2") {
		t.Errorf("blockers = %v, want the raised pod recorded", w.Blockers)
	}
}

func TestCandidateSavingsWithheldWhenAPodHasNoRecommendation(t *testing.T) {
	measured := overProvisionedPod("prod", "api-a", "Deployment", "api")
	unmeasured := overProvisionedPod("prod", "api-b", "Deployment", "api")
	unmeasured.CPUP95Available, unmeasured.MemP95Available = false, false

	w := decodeWorkloadEvidence(t, mustBuild(t, workloadReport(measured, unmeasured))).Workloads[0]

	if w.CandidateCPUSavingsMonthly != nil || w.CandidateAllocationSavingsMonthly != nil {
		t.Errorf("partial coverage must withhold savings, got %+v", w)
	}
	if !containsSubstring(w.Blockers, "1 of 2 analyzed pods have no cpu recommendation") {
		t.Errorf("blockers = %v, want the uncovered pod named", w.Blockers)
	}
}

func TestCandidateSavingsWithheldWhenReplicasWereNotAnalyzed(t *testing.T) {
	analyzed := overProvisionedPod("prod", "api-a", "Deployment", "api")

	info := infoFor(analyzed)
	info.Nodes[0].Pods = append(info.Nodes[0].Pods, collector.PodInfo{
		Name: "api-b", Namespace: "prod", OwnerKind: "Deployment", OwnerName: "api", CPURequest: 2000})
	info.TotalPods = 2

	w := decodeWorkloadEvidence(t, mustBuildWith(t, workloadReport(analyzed), info)).Workloads[0]

	if w.CandidateAllocationSavingsMonthly != nil {
		t.Errorf("a subset of replicas must not be priced as the workload, got %v",
			*w.CandidateAllocationSavingsMonthly)
	}
	if !containsSubstring(w.Blockers, "not every scheduled pod was analyzed") {
		t.Errorf("blockers = %v, want the missing replica recorded", w.Blockers)
	}
	if w.AdvisorPodSavingsMonthly == nil {
		t.Error("the advisor's own per-pod figure should still be reported")
	}
}

// No request means no rate, and the candidate would raise the pod from zero.
func TestCandidateSavingsWithheldWhenAPodRequestsNothing(t *testing.T) {
	sized := overProvisionedPod("prod", "api-a", "Deployment", "api")

	unsized := overProvisionedPod("prod", "api-b", "Deployment", "api")
	unsized.CPURequest, unsized.CPUCost, unsized.CPUP95Available = 0, 0, false

	w := decodeWorkloadEvidence(t, mustBuild(t, workloadReport(sized, unsized))).Workloads[0]

	if w.CandidateCPUMillicores == nil {
		t.Fatal("the sized pod should still produce a cpu candidate")
	}
	if w.CandidateCPUSavingsMonthly != nil || w.CandidateAllocationSavingsMonthly != nil {
		t.Errorf("an unpriceable pod must withhold savings, got %+v", w)
	}
	if !containsSubstring(w.Blockers, "request no cpu") {
		t.Errorf("blockers = %v, want the zero request named", w.Blockers)
	}

	// Memory is requested by both pods, so it is priced independently.
	if w.CandidateMemSavingsMonthly == nil {
		t.Error("memory should still be priced when only cpu is unpriceable")
	}
}

func TestCandidateSavingsAreIndependentPerResource(t *testing.T) {
	pod := overProvisionedPod("prod", "api-a", "Deployment", "api")
	pod.MemP95Available = false

	w := decodeWorkloadEvidence(t, mustBuild(t, workloadReport(pod))).Workloads[0]

	if w.CandidateMemBytes != nil || w.CandidateMemSavingsMonthly != nil {
		t.Errorf("memory was not recommended, so it must stay null: %+v", w)
	}
	if w.CandidateCPUSavingsMonthly == nil {
		t.Fatal("cpu should still be priced")
	}
	if !closeTo(*w.CandidateAllocationSavingsMonthly, *w.CandidateCPUSavingsMonthly) {
		t.Errorf("total = %v, want the cpu figure %v when memory is unchanged",
			*w.CandidateAllocationSavingsMonthly, *w.CandidateCPUSavingsMonthly)
	}
}

func TestCurrentRequestsPublishedOnlyWhenUniform(t *testing.T) {
	a := overProvisionedPod("prod", "api-a", "Deployment", "api")
	uniform := decodeWorkloadEvidence(t, mustBuild(t, workloadReport(a))).Workloads[0]

	if uniform.CurrentCPUMillicores == nil || *uniform.CurrentCPUMillicores != 2000 {
		t.Errorf("current cpu = %v, want 2000", uniform.CurrentCPUMillicores)
	}

	b := overProvisionedPod("prod", "api-b", "Deployment", "api")
	b.CPURequest = 4000
	mixed := decodeWorkloadEvidence(t, mustBuild(t, workloadReport(a, b))).Workloads[0]

	if mixed.CurrentCPUMillicores != nil || mixed.CurrentMemBytes != nil {
		t.Errorf("pods disagree on requests, so there is no single current value: %+v", mixed)
	}
}

func TestCandidateSavingsWithheldWhenInventoryIsUnavailable(t *testing.T) {
	report := workloadReport(overProvisionedPod("prod", "api-1", "Deployment", "api"))

	e, err := buildWorkloadEvidence(report, nil,
		advisor.CalculateSavings(report, advisor.DefaultSavingsConfig()), report.Period)
	if err != nil {
		t.Fatalf("buildWorkloadEvidence() error = %v", err)
	}
	got := decodeWorkloadEvidence(t, e)

	if !got.Coverage.InventoryUnavailable {
		t.Error("a missing cluster view should be declared, not assumed complete")
	}
	assertCoverageInvariants(t, got.Coverage)
	if got.Coverage.PodsAnalyzed != 1 {
		t.Errorf("pods_analyzed = %d, want 1", got.Coverage.PodsAnalyzed)
	}
	if !strings.Contains(string(e.Data), `"inventory_unavailable":true`) {
		t.Error("inventory_unavailable missing from the serialized evidence")
	}

	w := got.Workloads[0]
	if w.CandidateCPUMillicores == nil {
		t.Fatal("the candidate itself is still derivable from the analyzed pod")
	}
	if w.CandidateAllocationSavingsMonthly != nil ||
		w.CandidateCPUSavingsMonthly != nil || w.CandidateMemSavingsMonthly != nil {
		t.Errorf("unknown replica coverage must withhold savings, got %+v", w)
	}
	if !containsSubstring(w.Blockers, "cluster inventory unavailable") {
		t.Errorf("blockers = %v, want the missing inventory recorded", w.Blockers)
	}

	// The advisor figure does not depend on the replica count.
	if w.AdvisorPodSavingsMonthly == nil {
		t.Error("the advisor figure should still be reported")
	}
}
