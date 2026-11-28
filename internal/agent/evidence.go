package agent

import (
	"fmt"
	"sort"

	"github.com/tanrikuluozlem/burn/internal/advisor"
	"github.com/tanrikuluozlem/burn/internal/analyzer"
	"github.com/tanrikuluozlem/burn/internal/collector"
	"github.com/tanrikuluozlem/burn/internal/plan"
)

// Inputs a safety decision needs that Burn does not collect.
var notCollected = []string{
	"hpa",
	"rollout_strategy",
	"container_level_requests",
	"node_placement",
	"unscheduled_pod_ownership",
}

// Allocation-based, not verified against a bill. The factors below influence
// whether freed capacity reaches the bill; none guarantees it.
var savingsSemantics = savingsMeaning{
	Basis:                 "allocation",
	Stage:                 "identified",
	FinancialVerification: "not_performed",
	RealizationFactors: []string{
		"infrastructure_capacity_reduction",
		"avoided_capacity_growth",
		"commitment_coverage_and_utilization",
	},
}

type savingsMeaning struct {
	Basis                 string   `json:"basis"`
	Stage                 string   `json:"stage"`
	FinancialVerification string   `json:"financial_verification"`
	RealizationFactors    []string `json:"realization_factors"`
}

type workloadEvidence struct {
	Version          int                 `json:"version"`
	MetricsSource    string              `json:"metrics_source"`
	PeriodRequested  string              `json:"period_requested"`
	PeriodEffective  string              `json:"period_effective"`
	NotCollected     []string            `json:"not_collected"`
	SavingsSemantics savingsMeaning      `json:"savings_semantics"`
	Coverage         podCoverage         `json:"coverage"`
	Workloads        []workloadCandidate `json:"workloads"`
	Unattributed     []podCandidate      `json:"unattributed_pods,omitempty"`
}

// Exclusion categories are disjoint and sum to PodsExcluded. ExcludedOther is
// whatever the analyzer dropped for a reason not observable here.
type podCoverage struct {
	// InventoryUnavailable means no cluster view was supplied: the counts below
	// stay zero rather than inferred, and no workload is priced.
	InventoryUnavailable bool `json:"inventory_unavailable"`

	PodsDiscovered      int `json:"pods_discovered"`
	PodsAnalyzed        int `json:"pods_analyzed"`
	PodsExcluded        int `json:"pods_excluded"`
	PodsAttributed      int `json:"pods_attributed"`
	PodsUnattributed    int `json:"pods_unattributed"`
	ExcludedUnscheduled int `json:"excluded_unscheduled"`
	ExcludedNoRequests  int `json:"excluded_no_requests"`
	ExcludedOther       int `json:"excluded_other"`
}

// AdvisorPodSavingsMonthly is the savings engine's per-pod sum, which omits pods
// under its $1/mo minimum and so reconciles with `burn analyze`. The Candidate*
// figures price the single candidate against every pod.
type workloadCandidate struct {
	Kind                       string   `json:"kind"`
	Namespace                  string   `json:"namespace"`
	Name                       string   `json:"name"`
	PodsScheduled              int      `json:"pods_scheduled"`
	PodsAnalyzed               int      `json:"pods_analyzed"`
	PodsWithRecommendation     int      `json:"pods_with_recommendation"`
	PodsWithCPURecommendation  int      `json:"pods_with_cpu_recommendation"`
	PodsWithMemRecommendation  int      `json:"pods_with_mem_recommendation"`
	CurrentRequestsUniform     bool     `json:"current_requests_uniform"`
	CurrentCPUMillicores       *float64 `json:"current_cpu_millicores"`
	CurrentMemBytes            *float64 `json:"current_mem_bytes"`
	CandidateCPUMillicores     *float64 `json:"candidate_cpu_millicores"`
	CandidateMemBytes          *float64 `json:"candidate_mem_bytes"`
	AdvisorPodSavingsMonthly   *float64 `json:"advisor_pod_savings_monthly"`
	CandidateCPUSavingsMonthly *float64 `json:"candidate_cpu_savings_monthly"`
	CandidateMemSavingsMonthly *float64 `json:"candidate_mem_savings_monthly"`

	CandidateAllocationSavingsMonthly *float64 `json:"candidate_allocation_savings_monthly"`
	Blockers                          []string `json:"blockers,omitempty"`

	Pods []podCandidate `json:"pods"`
}

// Unmeasured values are pointers, so a missing value is null rather than zero.
type podCandidate struct {
	Name                     string   `json:"name"`
	Namespace                string   `json:"namespace"`
	CPURequestMillicores     int64    `json:"cpu_request_millicores"`
	MemRequestBytes          int64    `json:"mem_request_bytes"`
	CPUCostMonthly           float64  `json:"cpu_cost_monthly"`
	MemCostMonthly           float64  `json:"mem_cost_monthly"`
	CPUUsageCores            float64  `json:"cpu_usage_cores"`
	MemUsageBytes            int64    `json:"mem_usage_bytes"`
	CPUP95Cores              *float64 `json:"cpu_p95_cores"`
	MemP95Bytes              *int64   `json:"mem_p95_bytes"`
	RecommendedCPUMillicores *float64 `json:"recommended_cpu_millicores"`
	RecommendedMemBytes      *float64 `json:"recommended_mem_bytes"`
	CPUNoRecommendation      string   `json:"cpu_no_recommendation_reason,omitempty"`
	MemNoRecommendation      string   `json:"mem_no_recommendation_reason,omitempty"`
}

// CPU and memory have separate cost bases.
type allocation struct {
	name    string
	request func(podCandidate) float64
	cost    func(podCandidate) float64
}

var cpuAllocation = allocation{
	name:    "cpu",
	request: func(p podCandidate) float64 { return float64(p.CPURequestMillicores) },
	cost:    func(p podCandidate) float64 { return p.CPUCostMonthly },
}

var memAllocation = allocation{
	name:    "memory",
	request: func(p podCandidate) float64 { return float64(p.MemRequestBytes) },
	cost:    func(p podCandidate) float64 { return p.MemCostMonthly },
}

func buildWorkloadEvidence(report *analyzer.CostReport, info *collector.ClusterInfo, savings *advisor.PotentialSavings, requestedPeriod string) (plan.Evidence, error) {
	recommended := recommendationsByPod(savings)
	discovered, scheduled, noRequests := discoveredPods(info)

	byWorkload := make(map[string]*workloadCandidate)
	podSavings := make(map[string]float64)
	var unattributed []podCandidate

	for _, pod := range report.AllPods {
		candidate := newPodCandidate(pod, recommended)

		if pod.OwnerKind == "" || pod.OwnerName == "" {
			unattributed = append(unattributed, candidate)
			continue
		}

		key := workloadKey(pod.Namespace, pod.OwnerKind, pod.OwnerName)
		w, ok := byWorkload[key]
		if !ok {
			w = &workloadCandidate{Kind: pod.OwnerKind, Namespace: pod.Namespace, Name: pod.OwnerName}
			byWorkload[key] = w
		}
		w.Pods = append(w.Pods, candidate)

		if r, ok := recommended[pod.Namespace+"/"+pod.Name]; ok {
			podSavings[key] += r.CPUSavings + r.MemSavings
		}
	}

	// List fully excluded workloads so they read as unanalyzed, not absent.
	if info != nil {
		for _, w := range info.Workloads {
			key := workloadKey(w.Namespace, w.Kind, w.Name)
			if _, ok := byWorkload[key]; !ok {
				byWorkload[key] = &workloadCandidate{Kind: w.Kind, Namespace: w.Namespace, Name: w.Name}
			}
		}
	}

	keys := make([]string, 0, len(byWorkload))
	for k := range byWorkload {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	workloads := make([]workloadCandidate, 0, len(keys))
	for _, k := range keys {
		w := byWorkload[k]
		sortPods(w.Pods)
		summarize(w, discovered[k], info != nil, podSavings[k])
		workloads = append(workloads, *w)
	}
	sortPods(unattributed)

	return newEvidence(toolWorkloads, workloadEvidence{
		Version:          plan.WorkloadEvidenceVersion,
		MetricsSource:    report.MetricsSource,
		PeriodRequested:  requestedPeriod,
		PeriodEffective:  report.Period,
		NotCollected:     notCollected,
		SavingsSemantics: savingsSemantics,
		Coverage:         coverage(report, info, scheduled, noRequests, len(unattributed)),
		Workloads:        workloads,
		Unattributed:     unattributed,
	})
}

// Counts what the collector saw; the analyzer's drops are the difference.
func discoveredPods(info *collector.ClusterInfo) (byWorkload map[string]int, scheduled, noRequests int) {
	byWorkload = make(map[string]int)
	if info == nil {
		return byWorkload, 0, 0
	}

	for _, node := range info.Nodes {
		for _, pod := range node.Pods {
			scheduled++
			if pod.CPURequest == 0 && pod.MemoryRequest == 0 {
				noRequests++
			}
			if pod.OwnerKind != "" && pod.OwnerName != "" {
				byWorkload[workloadKey(pod.Namespace, pod.OwnerKind, pod.OwnerName)]++
			}
		}
	}
	return byWorkload, scheduled, noRequests
}

func coverage(report *analyzer.CostReport, info *collector.ClusterInfo, scheduled, noRequests, unattributed int) podCoverage {
	analyzed := len(report.AllPods)

	c := podCoverage{
		PodsAnalyzed:     analyzed,
		PodsAttributed:   analyzed - unattributed,
		PodsUnattributed: unattributed,
	}
	if info == nil {
		c.InventoryUnavailable = true
		return c
	}

	c.PodsDiscovered = info.TotalPods
	c.PodsExcluded = info.TotalPods - analyzed
	c.ExcludedUnscheduled = info.TotalPods - scheduled
	c.ExcludedNoRequests = noRequests
	c.ExcludedOther = scheduled - analyzed - noRequests
	return c
}

// A controller holds one pod template, so the pods reduce to one candidate per
// resource. The largest recommendation is taken so no pod is set below its own.
func summarize(w *workloadCandidate, discovered int, inventoryKnown bool, advisorSavings float64) {
	w.PodsAnalyzed = len(w.Pods)
	w.PodsScheduled = discovered
	if discovered < w.PodsAnalyzed {
		w.PodsScheduled = w.PodsAnalyzed
	}
	w.CurrentRequestsUniform = true

	var maxCPU, maxMem float64
	var haveCPU, haveMem bool

	for i, pod := range w.Pods {
		// Uniform requests, not recommendations.
		if i > 0 && (pod.CPURequestMillicores != w.Pods[0].CPURequestMillicores ||
			pod.MemRequestBytes != w.Pods[0].MemRequestBytes) {
			w.CurrentRequestsUniform = false
		}

		if pod.RecommendedCPUMillicores != nil {
			w.PodsWithCPURecommendation++
			if !haveCPU || *pod.RecommendedCPUMillicores > maxCPU {
				maxCPU, haveCPU = *pod.RecommendedCPUMillicores, true
			}
		}
		if pod.RecommendedMemBytes != nil {
			w.PodsWithMemRecommendation++
			if !haveMem || *pod.RecommendedMemBytes > maxMem {
				maxMem, haveMem = *pod.RecommendedMemBytes, true
			}
		}
		if pod.RecommendedCPUMillicores != nil || pod.RecommendedMemBytes != nil {
			w.PodsWithRecommendation++
		}
	}

	if haveCPU {
		w.CandidateCPUMillicores = &maxCPU
	}
	if haveMem {
		w.CandidateMemBytes = &maxMem
	}
	if w.PodsAnalyzed > 0 {
		w.AdvisorPodSavingsMonthly = &advisorSavings
		if w.CurrentRequestsUniform {
			cpu, mem := float64(w.Pods[0].CPURequestMillicores), float64(w.Pods[0].MemRequestBytes)
			w.CurrentCPUMillicores, w.CurrentMemBytes = &cpu, &mem
		}
	}

	switch {
	case w.PodsAnalyzed == 0:
		w.Blockers = append(w.Blockers, "no analyzable pods")
	case w.PodsWithRecommendation == 0:
		w.Blockers = append(w.Blockers, "no pod-level recommendation")
	case w.PodsWithRecommendation < w.PodsAnalyzed:
		w.Blockers = append(w.Blockers,
			fmt.Sprintf("recommendation covers %d of %d analyzed pods", w.PodsWithRecommendation, w.PodsAnalyzed))
	}
	switch {
	case !inventoryKnown:
		w.Blockers = append(w.Blockers,
			"scheduled pod count unknown: cluster inventory unavailable")
	case w.PodsAnalyzed < w.PodsScheduled:
		w.Blockers = append(w.Blockers,
			fmt.Sprintf("%d of %d scheduled pods excluded from analysis", w.PodsScheduled-w.PodsAnalyzed, w.PodsScheduled))
	}
	if !w.CurrentRequestsUniform {
		w.Blockers = append(w.Blockers, "pods do not share the same requests")
	}

	priceCandidates(w, inventoryKnown)
}

// Requires full replica coverage.
func priceCandidates(w *workloadCandidate, inventoryKnown bool) {
	everyReplicaAnalyzed := inventoryKnown && w.PodsAnalyzed > 0 && w.PodsAnalyzed == w.PodsScheduled

	cpu, cpuBlockers := candidateSavings(w.Pods, w.CandidateCPUMillicores, cpuAllocation,
		w.PodsWithCPURecommendation, everyReplicaAnalyzed)
	mem, memBlockers := candidateSavings(w.Pods, w.CandidateMemBytes, memAllocation,
		w.PodsWithMemRecommendation, everyReplicaAnalyzed)

	w.CandidateCPUSavingsMonthly, w.CandidateMemSavingsMonthly = cpu, mem
	w.Blockers = append(w.Blockers, cpuBlockers...)
	w.Blockers = append(w.Blockers, memBlockers...)

	proposed := w.CandidateCPUMillicores != nil || w.CandidateMemBytes != nil
	cpuPriced := w.CandidateCPUMillicores == nil || cpu != nil
	memPriced := w.CandidateMemBytes == nil || mem != nil
	if proposed && cpuPriced && memPriced {
		total := deref(cpu) + deref(mem)
		w.CandidateAllocationSavingsMonthly = &total
	}
}

// cost/request assumes usage < request, which the engine's efficiency threshold
// guarantees for every pod counted here.
func candidateSavings(pods []podCandidate, candidate *float64, r allocation, covered int, everyReplicaAnalyzed bool) (*float64, []string) {
	if candidate == nil {
		return nil, nil
	}

	var withheld []string
	if !everyReplicaAnalyzed {
		withheld = append(withheld, fmt.Sprintf(
			"%s candidate savings withheld: not every scheduled pod was analyzed", r.name))
	}
	if covered < len(pods) {
		withheld = append(withheld, fmt.Sprintf(
			"%s candidate savings withheld: %d of %d analyzed pods have no %s recommendation",
			r.name, len(pods)-covered, len(pods), r.name))
	}

	// No request means no rate. Raising the pod from zero to the candidate has a
	// cost that cannot be derived here.
	var unpriceable int
	for _, pod := range pods {
		if r.request(pod) <= 0 {
			unpriceable++
		}
	}
	if unpriceable > 0 {
		withheld = append(withheld, fmt.Sprintf(
			"%s candidate savings withheld: %d of %d analyzed pods request no %s, so raising them to the candidate cannot be priced from this evidence",
			r.name, unpriceable, len(pods), r.name))
	}
	if len(withheld) > 0 {
		return nil, withheld
	}

	var total float64
	var raised int

	for _, pod := range pods {
		request := r.request(pod)
		if *candidate > request {
			raised++
		}
		total += r.cost(pod) * (request - *candidate) / request
	}

	var blockers []string
	if raised > 0 {
		blockers = append(blockers, fmt.Sprintf(
			"%s candidate is above the current request of %d of %d analyzed pods", r.name, raised, len(pods)))
	}
	return &total, blockers
}

func deref(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}

func recommendationsByPod(savings *advisor.PotentialSavings) map[string]advisor.PodSaving {
	index := make(map[string]advisor.PodSaving)
	if savings == nil || savings.RightSizing == nil {
		return index
	}
	for _, p := range savings.RightSizing.Pods {
		index[p.Namespace+"/"+p.Name] = p
	}
	return index
}

func newPodCandidate(pod analyzer.PodEfficiency, recommended map[string]advisor.PodSaving) podCandidate {
	c := podCandidate{
		Name:                 pod.Name,
		Namespace:            pod.Namespace,
		CPURequestMillicores: pod.CPURequest,
		MemRequestBytes:      pod.MemRequest,
		CPUCostMonthly:       pod.CPUCost,
		MemCostMonthly:       pod.RAMCost,
		CPUUsageCores:        pod.CPUUsage,
		MemUsageBytes:        pod.MemUsage,
	}

	if pod.CPUP95Available {
		cpu := pod.CPUP95Usage
		c.CPUP95Cores = &cpu
	}
	if pod.MemP95Available {
		mem := pod.MemoryP95Usage
		c.MemP95Bytes = &mem
	}

	r, ok := recommended[pod.Namespace+"/"+pod.Name]
	if ok && r.RecommendedCPUMillicores > 0 {
		cpu := r.RecommendedCPUMillicores
		c.RecommendedCPUMillicores = &cpu
	}
	if ok && r.RecommendedMemBytes > 0 {
		mem := r.RecommendedMemBytes
		c.RecommendedMemBytes = &mem
	}
	if c.RecommendedCPUMillicores == nil {
		c.CPUNoRecommendation = declineReason(pod.CPUP95Available, pod.CPUCost)
	}
	if c.RecommendedMemBytes == nil {
		c.MemNoRecommendation = declineReason(pod.MemP95Available, pod.RAMCost)
	}
	return c
}

// Efficiency and minimum-savings declines cannot be told apart without
// repeating the engine's arithmetic, so both report engine_declined.
func declineReason(p95Available bool, cost float64) string {
	switch {
	case !p95Available:
		return "p95_missing"
	case cost == 0:
		return "no_cost"
	default:
		return "engine_declined"
	}
}

func workloadKey(namespace, kind, name string) string {
	return namespace + "/" + kind + "/" + name
}

func sortPods(pods []podCandidate) {
	sort.Slice(pods, func(i, j int) bool {
		if pods[i].Namespace != pods[j].Namespace {
			return pods[i].Namespace < pods[j].Namespace
		}
		return pods[i].Name < pods[j].Name
	})
}
