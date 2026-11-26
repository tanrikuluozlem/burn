package plan

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"time"
)

// SchemaVersion is refused, not migrated, when it does not match.
const SchemaVersion = 2

const (
	// SavingsTool records cluster-wide totals, never a single workload's.
	SavingsTool = "savings"
	// WorkloadsTool records per-workload candidates, which expected_savings
	// must cite.
	WorkloadsTool = "workloads"
)

// WorkloadEvidenceVersion is the layout this package can read; another version
// is refused.
const WorkloadEvidenceVersion = 3

// ReadinessInvestigation is the only readiness a plan may claim: container-level
// requests, HPA, rollout strategy and placement are not collected.
const ReadinessInvestigation = "investigation"

type Plan struct {
	SchemaVersion int       `json:"schema_version"`
	ID            string    `json:"id"`
	CreatedAt     time.Time `json:"created_at"`
	Goal          string    `json:"goal"`
	Readiness     string    `json:"readiness"`
	Target        Target    `json:"target"`
	Current       Resources `json:"current"`
	Proposed      Resources `json:"proposed"`
	// Null when the evidence supports no figure.
	ExpectedSavings *Value `json:"expected_savings"`
	Confidence      string `json:"confidence"`
	// Copied from the cited workload and checked against it on validation.
	Blockers []string `json:"blockers"`
	// Copied from the evidence's not_collected list.
	UnsupportedPrerequisites []string `json:"unsupported_prerequisites"`
}

type Target struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

type Resources struct {
	CPUMillicores *Value `json:"cpu_millicores"`
	MemoryBytes   *Value `json:"memory_bytes"`
}

// Checking a Value proves consistency with the ledger, not authenticity; the
// ledger must come from the tool layer.
type Value struct {
	Amount float64     `json:"amount"`
	Source EvidenceRef `json:"source"`
}

type EvidenceRef struct {
	EvidenceID string `json:"evidence_id"`
	Path       string `json:"path"`
}

type Evidence struct {
	ID   string          `json:"id"`
	Tool string          `json:"tool"`
	Data json.RawMessage `json:"data"`
}

// EvidenceID derives identity from content. The length prefix separates tool
// from data so ("ab","c") and ("a","bc") do not hash the same bytes.
func EvidenceID(tool string, data []byte) string {
	var prefix [8]byte
	binary.BigEndian.PutUint64(prefix[:], uint64(len(tool)))

	h := sha256.New()
	h.Write(prefix[:])
	h.Write([]byte(tool))
	h.Write(data)

	return hex.EncodeToString(h.Sum(nil)[:8])
}

// Hash identifies exact plan content, so a later edit is detectable.
func (p *Plan) Hash() (string, error) {
	data, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
