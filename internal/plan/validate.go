package plan

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

var confidenceLevels = map[string]bool{"low": true, "medium": true, "high": true}

// The field each plan number must come from; a fixed suffix stops a plausible
// value being read off a neighbour.
const (
	pathCurrentCPU   = "current_cpu_millicores"
	pathCurrentMem   = "current_mem_bytes"
	pathCandidateCPU = "candidate_cpu_millicores"
	pathCandidateMem = "candidate_mem_bytes"
	pathSavings      = "candidate_allocation_savings_monthly"
)

// Plan and evidence numbers arrive by different arithmetic and through JSON, so
// agreement is a tolerance. The absolute floor covers values near zero.
const (
	relTolerance = 1e-9
	absTolerance = 1e-9
)

// internal/agent owns the full shape; repeating a few field names here keeps
// the dependency pointing one way.
type workloadDoc struct {
	Version      int           `json:"version"`
	NotCollected []string      `json:"not_collected"`
	Workloads    []workloadRef `json:"workloads"`
}

type workloadRef struct {
	Kind      string   `json:"kind"`
	Namespace string   `json:"namespace"`
	Name      string   `json:"name"`
	Blockers  []string `json:"blockers"`
}

// anchor is the one workload every number in a plan must come from.
type anchor struct {
	evidenceID string
	index      int
}

func (a anchor) String() string {
	return fmt.Sprintf("%s workloads.%d", a.evidenceID, a.index)
}

// Validate resolves every number back to the evidence it cites and confirms
// that evidence is about the target workload.
func Validate(p *Plan, ledger []Evidence) error {
	if p.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported schema version %d, want %d", p.SchemaVersion, SchemaVersion)
	}
	if p.ID == "" {
		return fmt.Errorf("plan id required")
	}
	if p.Goal == "" {
		return fmt.Errorf("goal required")
	}
	if p.Readiness != ReadinessInvestigation {
		return fmt.Errorf("readiness %q is not supported, want %q: execution prerequisites are not collected",
			p.Readiness, ReadinessInvestigation)
	}
	if p.Target.Kind == "" || p.Target.Namespace == "" || p.Target.Name == "" {
		return fmt.Errorf("target kind, namespace and name required")
	}
	if !confidenceLevels[p.Confidence] {
		return fmt.Errorf("invalid confidence %q", p.Confidence)
	}

	index := make(map[string]Evidence, len(ledger))
	for _, e := range ledger {
		if e.ID != EvidenceID(e.Tool, e.Data) {
			return fmt.Errorf("evidence %q does not match its content", e.ID)
		}
		if _, seen := index[e.ID]; seen {
			return fmt.Errorf("evidence %q appears twice in the ledger", e.ID)
		}
		index[e.ID] = e
	}

	cited := []struct {
		name   string
		value  *Value
		suffix string
	}{
		{"current.cpu_millicores", p.Current.CPUMillicores, pathCurrentCPU},
		{"current.memory_bytes", p.Current.MemoryBytes, pathCurrentMem},
		{"proposed.cpu_millicores", p.Proposed.CPUMillicores, pathCandidateCPU},
		{"proposed.memory_bytes", p.Proposed.MemoryBytes, pathCandidateMem},
		{"expected_savings", p.ExpectedSavings, pathSavings},
	}

	var at *anchor
	for _, f := range cited {
		if f.value == nil {
			continue
		}
		got, err := checkCitation(*f.value, f.suffix, index)
		if err != nil {
			return fmt.Errorf("%s: %w", f.name, err)
		}
		if at == nil {
			at = &got
			continue
		}
		if got != *at {
			return fmt.Errorf("%s cites %s, but the rest of the plan cites %s", f.name, got, *at)
		}
	}
	if at == nil {
		return fmt.Errorf("plan cites no workload evidence")
	}

	doc, w, err := workloadAt(index[at.evidenceID], at.index)
	if err != nil {
		return err
	}
	if w.Kind != p.Target.Kind || w.Namespace != p.Target.Namespace || w.Name != p.Target.Name {
		return fmt.Errorf("plan targets %s %s/%s but cites evidence for %s %s/%s",
			p.Target.Kind, p.Target.Namespace, p.Target.Name, w.Kind, w.Namespace, w.Name)
	}
	if !sameStrings(p.Blockers, w.Blockers) {
		return fmt.Errorf("blockers %v do not match the cited workload's %v", p.Blockers, w.Blockers)
	}
	if !sameStrings(p.UnsupportedPrerequisites, doc.NotCollected) {
		return fmt.Errorf("unsupported_prerequisites %v do not match the evidence's not_collected %v",
			p.UnsupportedPrerequisites, doc.NotCollected)
	}
	return nil
}

// Returns which workload the number came from, so the caller can require the
// rest to agree.
func checkCitation(v Value, suffix string, index map[string]Evidence) (anchor, error) {
	if v.Source.EvidenceID == "" {
		return anchor{}, fmt.Errorf("no evidence cited")
	}
	e, ok := index[v.Source.EvidenceID]
	if !ok {
		return anchor{}, fmt.Errorf("evidence %q not in ledger", v.Source.EvidenceID)
	}
	if e.Tool != WorkloadsTool {
		return anchor{}, fmt.Errorf("cites %q evidence, want %q: a workload's numbers cannot come from a cluster-wide record",
			e.Tool, WorkloadsTool)
	}
	i, err := workloadIndex(v.Source.Path, suffix)
	if err != nil {
		return anchor{}, err
	}
	if !finite(v.Amount) {
		return anchor{}, fmt.Errorf("claims a value that is not a finite number")
	}

	got, err := resolve(e.Data, v.Source.Path)
	if err != nil {
		return anchor{}, err
	}
	if !nearlyEqual(got, v.Amount) {
		return anchor{}, fmt.Errorf("claims %v but evidence %s holds %v", v.Amount, v.Source.EvidenceID, got)
	}
	return anchor{evidenceID: v.Source.EvidenceID, index: i}, nil
}

func workloadIndex(path, suffix string) (int, error) {
	parts := strings.Split(path, ".")
	if len(parts) != 3 || parts[0] != "workloads" || parts[2] != suffix {
		return 0, fmt.Errorf("path %q must be workloads.<index>.%s", path, suffix)
	}
	i, err := strconv.Atoi(parts[1])
	if err != nil || i < 0 {
		return 0, fmt.Errorf("path %q has an invalid workload index %q", path, parts[1])
	}
	return i, nil
}

func workloadAt(e Evidence, i int) (workloadDoc, workloadRef, error) {
	var doc workloadDoc
	if err := json.Unmarshal(e.Data, &doc); err != nil {
		return doc, workloadRef{}, fmt.Errorf("decode workload evidence %s: %w", e.ID, err)
	}
	if doc.Version != WorkloadEvidenceVersion {
		return doc, workloadRef{}, fmt.Errorf("workload evidence %s is version %d, want %d",
			e.ID, doc.Version, WorkloadEvidenceVersion)
	}
	if i >= len(doc.Workloads) {
		return doc, workloadRef{}, fmt.Errorf("workload index %d is out of range, evidence %s holds %d",
			i, e.ID, len(doc.Workloads))
	}
	return doc, doc.Workloads[i], nil
}

// resolve walks a dot-separated path; numeric segments index arrays.
func resolve(data json.RawMessage, path string) (float64, error) {
	if path == "" {
		return 0, fmt.Errorf("empty evidence path")
	}

	var node any
	if err := json.Unmarshal(data, &node); err != nil {
		return 0, fmt.Errorf("decode evidence: %w", err)
	}

	for _, segment := range strings.Split(path, ".") {
		switch typed := node.(type) {
		case map[string]any:
			child, ok := typed[segment]
			if !ok {
				return 0, fmt.Errorf("path %q not found at %q", path, segment)
			}
			node = child
		case []any:
			i, err := strconv.Atoi(segment)
			if err != nil || i < 0 || i >= len(typed) {
				return 0, fmt.Errorf("path %q has invalid index %q", path, segment)
			}
			node = typed[i]
		default:
			return 0, fmt.Errorf("path %q continues past a leaf at %q", path, segment)
		}
	}

	if node == nil {
		return 0, fmt.Errorf("path %q is null: the evidence supports no value there", path)
	}
	number, ok := node.(float64)
	if !ok {
		return 0, fmt.Errorf("path %q is not a number", path)
	}
	if !finite(number) {
		return 0, fmt.Errorf("path %q is not a finite number", path)
	}
	return number, nil
}

func finite(f float64) bool {
	return !math.IsNaN(f) && !math.IsInf(f, 0)
}

func nearlyEqual(a, b float64) bool {
	if a == b {
		return true
	}
	scale := math.Max(math.Abs(a), math.Abs(b))
	return math.Abs(a-b) <= math.Max(absTolerance, relTolerance*scale)
}

// Evidence omits an empty blocker list, so nil and empty are the same thing.
func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
