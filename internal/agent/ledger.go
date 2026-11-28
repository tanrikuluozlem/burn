package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/tanrikuluozlem/burn/internal/plan"
)

const (
	// Large enough for one workload, small enough to bound model context.
	maxFragmentBytes = 8 << 10
	maxWorkloadPage  = 25
)

// ledger holds tool output. Summaries sent to the model are navigation aids;
// plan.Validate resolves numbers against these records.
type ledger struct {
	records []plan.Evidence
	byID    map[string]int
}

func newLedger() *ledger {
	return &ledger{byID: make(map[string]int)}
}

// Append verifies content addressing. The same record twice is accepted once;
// one id over two payloads is rejected.
func (l *ledger) Append(e plan.Evidence) error {
	if e.ID != plan.EvidenceID(e.Tool, e.Data) {
		return fmt.Errorf("evidence %q does not match its content", e.ID)
	}
	if i, ok := l.byID[e.ID]; ok {
		if !bytes.Equal(l.records[i].Data, e.Data) {
			return fmt.Errorf("evidence %q already holds different content", e.ID)
		}
		return nil
	}
	l.byID[e.ID] = len(l.records)
	l.records = append(l.records, e)
	return nil
}

func (l *ledger) Records() []plan.Evidence {
	out := make([]plan.Evidence, len(l.records))
	copy(out, l.records)
	return out
}

type workloadQuery struct {
	Namespace string `json:"namespace,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Name      string `json:"name,omitempty"`
	Offset    int    `json:"offset,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

// workloadRef carries identity and the path a plan cites, not figures.
type workloadRef struct {
	EvidenceID   string `json:"evidence_id"`
	Path         string `json:"path"`
	Kind         string `json:"kind"`
	Namespace    string `json:"namespace"`
	Name         string `json:"name"`
	HasCandidate bool   `json:"has_candidate"`
	BlockerCount int    `json:"blocker_count"`
}

type workloadPage struct {
	Workloads  []workloadRef `json:"workloads"`
	Offset     int           `json:"offset"`
	Total      int           `json:"total"`
	NextOffset *int          `json:"next_offset"`
}

func (l *ledger) ListWorkloads(q workloadQuery) (workloadPage, error) {
	var all []workloadRef
	for _, rec := range l.records {
		if rec.Tool != plan.WorkloadsTool {
			continue
		}
		var doc workloadEvidence
		if err := json.Unmarshal(rec.Data, &doc); err != nil {
			return workloadPage{}, fmt.Errorf("decode evidence %s: %w", rec.ID, err)
		}
		for i, w := range doc.Workloads {
			if !matches(q, w) {
				continue
			}
			all = append(all, workloadRef{
				EvidenceID:   rec.ID,
				Path:         fmt.Sprintf("workloads.%d", i),
				Kind:         w.Kind,
				Namespace:    w.Namespace,
				Name:         w.Name,
				HasCandidate: w.CandidateCPUMillicores != nil || w.CandidateMemBytes != nil,
				BlockerCount: len(w.Blockers),
			})
		}
	}

	// Total order keeps page boundaries stable: one namespace can hold the same
	// name under two kinds.
	sort.Slice(all, func(i, j int) bool {
		a, b := all[i], all[j]
		switch {
		case a.Namespace != b.Namespace:
			return a.Namespace < b.Namespace
		case a.Name != b.Name:
			return a.Name < b.Name
		case a.Kind != b.Kind:
			return a.Kind < b.Kind
		case a.EvidenceID != b.EvidenceID:
			return a.EvidenceID < b.EvidenceID
		}
		return a.Path < b.Path
	})

	limit := q.Limit
	if limit <= 0 || limit > maxWorkloadPage {
		limit = maxWorkloadPage
	}
	offset := max(q.Offset, 0)
	page := workloadPage{Offset: offset, Total: len(all), Workloads: []workloadRef{}}
	if offset >= len(all) {
		return page, nil
	}

	end := min(offset+limit, len(all))
	page.Workloads = all[offset:end]
	if end < len(all) {
		page.NextOffset = &end
	}
	return page, nil
}

func matches(q workloadQuery, w workloadCandidate) bool {
	return (q.Namespace == "" || q.Namespace == w.Namespace) &&
		(q.Kind == "" || q.Kind == w.Kind) &&
		(q.Name == "" || q.Name == w.Name)
}

type evidenceQuery struct {
	EvidenceID string `json:"evidence_id"`
	Path       string `json:"path,omitempty"`
}

// evidenceFragment always holds valid JSON. An oversized node is described by
// shape rather than cut mid-value.
type evidenceFragment struct {
	EvidenceID string          `json:"evidence_id"`
	Path       string          `json:"path"`
	Data       json.RawMessage `json:"data"`
	Oversized  bool            `json:"oversized,omitempty"`
	Note       string          `json:"note,omitempty"`
}

func (l *ledger) GetEvidence(q evidenceQuery) (evidenceFragment, error) {
	i, ok := l.byID[q.EvidenceID]
	if !ok {
		return evidenceFragment{}, fmt.Errorf("evidence %q not in ledger", q.EvidenceID)
	}

	node, err := walk(l.records[i].Data, q.Path)
	if err != nil {
		return evidenceFragment{}, err
	}

	frag := evidenceFragment{EvidenceID: q.EvidenceID, Path: q.Path}
	data, err := json.Marshal(node)
	if err != nil {
		return evidenceFragment{}, fmt.Errorf("encode %q: %w", q.Path, err)
	}
	if len(data) <= maxFragmentBytes {
		frag.Data = data
		return frag, nil
	}

	frag.Oversized = true
	switch typed := node.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for k := range typed {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		frag.Data, err = json.Marshal(keys)
		frag.Note = "object too large; request one of these keys"
	case []any:
		frag.Data, err = json.Marshal(len(typed))
		frag.Note = "array too large; request one index"
	default:
		return evidenceFragment{}, fmt.Errorf("path %q is too large to return", q.Path)
	}
	if err != nil {
		return evidenceFragment{}, fmt.Errorf("encode shape of %q: %w", q.Path, err)
	}
	return frag, nil
}

// walk resolves a dot-separated path to a node; an empty path is the document.
// plan.resolve walks the same syntax but yields only numbers.
func walk(data json.RawMessage, path string) (any, error) {
	var node any
	if err := json.Unmarshal(data, &node); err != nil {
		return nil, fmt.Errorf("decode evidence: %w", err)
	}
	if path == "" {
		return node, nil
	}

	for _, segment := range strings.Split(path, ".") {
		switch typed := node.(type) {
		case map[string]any:
			child, ok := typed[segment]
			if !ok {
				return nil, fmt.Errorf("path %q not found at %q", path, segment)
			}
			node = child
		case []any:
			i, err := strconv.Atoi(segment)
			if err != nil || i < 0 || i >= len(typed) {
				return nil, fmt.Errorf("path %q has invalid index %q", path, segment)
			}
			node = typed[i]
		default:
			return nil, fmt.Errorf("path %q continues past a leaf at %q", path, segment)
		}
	}
	return node, nil
}
