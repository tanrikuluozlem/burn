package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tanrikuluozlem/burn/internal/analyzer"
	"github.com/tanrikuluozlem/burn/internal/plan"
)

func workloadRecord(t *testing.T, pods ...analyzer.PodEfficiency) plan.Evidence {
	t.Helper()
	return mustBuild(t, workloadReport(pods...))
}

func TestLedgerVerifiesContentOnInsert(t *testing.T) {
	l := newLedger()
	good := workloadRecord(t, overProvisionedPod("prod", "api-1", "Deployment", "api"))

	if err := l.Append(good); err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	tampered := good
	tampered.Data = json.RawMessage(`{"version":3,"workloads":[]}`)
	err := l.Append(tampered)
	if err == nil {
		t.Fatal("expected an id that no longer matches its content to be rejected")
	}
	if !strings.Contains(err.Error(), "does not match its content") {
		t.Errorf("error %q should report the content mismatch", err)
	}
	if n := len(l.Records()); n != 1 {
		t.Errorf("records = %d, want the original kept", n)
	}
}

// One id over two payloads needs a hash collision, so the ledger is seeded
// directly.
func TestLedgerRefusesOneIDOverTwoPayloads(t *testing.T) {
	first := workloadRecord(t, overProvisionedPod("prod", "api-1", "Deployment", "api"))
	second := workloadRecord(t, overProvisionedPod("db", "pg-0", "StatefulSet", "pg"))

	// The stored record holds first's bytes under second's id, so the arriving
	// record still addresses its own content.
	stored := plan.Evidence{ID: second.ID, Tool: first.Tool, Data: first.Data}
	l := newLedger()
	l.records = []plan.Evidence{stored}
	l.byID = map[string]int{stored.ID: 0}

	err := l.Append(second)
	if err == nil {
		t.Fatal("expected one id over two payloads to be rejected")
	}
	if !strings.Contains(err.Error(), "different content") {
		t.Errorf("error %q should report the conflict", err)
	}
}

func TestLedgerIsIdempotent(t *testing.T) {
	l := newLedger()
	rec := workloadRecord(t, overProvisionedPod("prod", "api-1", "Deployment", "api"))

	for range 3 {
		if err := l.Append(rec); err != nil {
			t.Fatalf("Append() error = %v", err)
		}
	}
	if n := len(l.Records()); n != 1 {
		t.Errorf("records = %d, want 1 after repeating the same record", n)
	}
}

func TestLedgerRejectsForgedEvidence(t *testing.T) {
	l := newLedger()
	err := l.Append(plan.Evidence{ID: "deadbeef", Tool: plan.WorkloadsTool, Data: json.RawMessage(`{}`)})
	if err == nil {
		t.Fatal("expected an id that does not address its content to be rejected")
	}
}

func manyWorkloads(t *testing.T, n int) plan.Evidence {
	t.Helper()
	pods := make([]analyzer.PodEfficiency, 0, n)
	for i := range n {
		pods = append(pods, overProvisionedPod("prod", "api-"+string(rune('a'+i)), "Deployment", "api-"+string(rune('a'+i))))
	}
	return workloadRecord(t, pods...)
}

func TestListWorkloadsPagesThroughEverything(t *testing.T) {
	l := newLedger()
	if err := l.Append(manyWorkloads(t, 30)); err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	seen := map[string]bool{}
	offset, pages := 0, 0
	for {
		page, err := l.ListWorkloads(workloadQuery{Offset: offset, Limit: 10})
		if err != nil {
			t.Fatalf("ListWorkloads() error = %v", err)
		}
		pages++
		if page.Total != 30 {
			t.Errorf("total = %d, want 30", page.Total)
		}
		if len(page.Workloads) > 10 {
			t.Errorf("page holds %d workloads, want at most 10", len(page.Workloads))
		}
		for _, w := range page.Workloads {
			seen[w.Name] = true
		}
		if page.NextOffset == nil {
			break
		}
		offset = *page.NextOffset
	}

	if len(seen) != 30 {
		t.Errorf("saw %d workloads across %d pages, want all 30", len(seen), pages)
	}
}

func TestListWorkloadsCapsThePageSize(t *testing.T) {
	l := newLedger()
	if err := l.Append(manyWorkloads(t, 30)); err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	page, err := l.ListWorkloads(workloadQuery{Limit: 1000})
	if err != nil {
		t.Fatalf("ListWorkloads() error = %v", err)
	}
	if len(page.Workloads) != maxWorkloadPage {
		t.Errorf("page holds %d, want the cap of %d", len(page.Workloads), maxWorkloadPage)
	}
}

func TestListWorkloadsFiltersByIdentity(t *testing.T) {
	l := newLedger()
	rec := workloadRecord(t,
		overProvisionedPod("prod", "api-1", "Deployment", "api"),
		overProvisionedPod("db", "pg-0", "StatefulSet", "pg"),
	)
	if err := l.Append(rec); err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	page, err := l.ListWorkloads(workloadQuery{Namespace: "db"})
	if err != nil {
		t.Fatalf("ListWorkloads() error = %v", err)
	}
	if len(page.Workloads) != 1 || page.Workloads[0].Name != "pg" {
		t.Fatalf("filtered page = %+v, want only pg", page.Workloads)
	}
	if page.Workloads[0].EvidenceID != rec.ID {
		t.Errorf("evidence id = %q, want the original %q", page.Workloads[0].EvidenceID, rec.ID)
	}
}

func TestListedPathResolvesInTheRecord(t *testing.T) {
	l := newLedger()
	rec := workloadRecord(t, overProvisionedPod("prod", "api-1", "Deployment", "api"))
	if err := l.Append(rec); err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	page, err := l.ListWorkloads(workloadQuery{})
	if err != nil {
		t.Fatalf("ListWorkloads() error = %v", err)
	}
	ref := page.Workloads[0]

	frag, err := l.GetEvidence(evidenceQuery{EvidenceID: ref.EvidenceID, Path: ref.Path + ".name"})
	if err != nil {
		t.Fatalf("GetEvidence() error = %v", err)
	}
	if string(frag.Data) != `"api"` {
		t.Errorf("data = %s, want the workload name", frag.Data)
	}
	if frag.Path != ref.Path+".name" {
		t.Errorf("path = %q, want it echoed back unchanged", frag.Path)
	}
}

func TestGetEvidenceRejectsUnknownIDAndPath(t *testing.T) {
	l := newLedger()
	rec := workloadRecord(t, overProvisionedPod("prod", "api-1", "Deployment", "api"))
	if err := l.Append(rec); err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	if _, err := l.GetEvidence(evidenceQuery{EvidenceID: "nope"}); err == nil {
		t.Error("expected an unknown evidence id to be rejected")
	}
	if _, err := l.GetEvidence(evidenceQuery{EvidenceID: rec.ID, Path: "workloads.99"}); err == nil {
		t.Error("expected an out-of-range index to be rejected")
	}
}

func TestGetEvidenceKeepsFragmentsValidJSON(t *testing.T) {
	l := newLedger()
	rec := manyWorkloads(t, 30)
	if err := l.Append(rec); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if len(rec.Data) <= maxFragmentBytes {
		t.Fatalf("fixture is only %d bytes; it must exceed the %d cap", len(rec.Data), maxFragmentBytes)
	}

	root, err := l.GetEvidence(evidenceQuery{EvidenceID: rec.ID})
	if err != nil {
		t.Fatalf("GetEvidence() error = %v", err)
	}
	if !root.Oversized {
		t.Error("the whole document should be reported as oversized")
	}
	var keys []string
	if err := json.Unmarshal(root.Data, &keys); err != nil {
		t.Fatalf("oversized object must still return valid JSON: %v", err)
	}
	if len(keys) == 0 {
		t.Error("expected the object's keys so the model can narrow the path")
	}

	list, err := l.GetEvidence(evidenceQuery{EvidenceID: rec.ID, Path: "workloads"})
	if err != nil {
		t.Fatalf("GetEvidence() error = %v", err)
	}
	var count int
	if err := json.Unmarshal(list.Data, &count); err != nil {
		t.Fatalf("oversized array must still return valid JSON: %v", err)
	}
	if count != 30 {
		t.Errorf("array length = %d, want 30", count)
	}

	one, err := l.GetEvidence(evidenceQuery{EvidenceID: rec.ID, Path: "workloads.0"})
	if err != nil {
		t.Fatalf("GetEvidence() error = %v", err)
	}
	if one.Oversized {
		t.Error("a single workload should fit")
	}
	if !json.Valid(one.Data) {
		t.Error("fragment is not valid JSON")
	}
}

// One namespace can hold the same name under two kinds.
func TestListWorkloadsOrdersDeterministically(t *testing.T) {
	l := newLedger()
	rec := workloadRecord(t,
		overProvisionedPod("prod", "api-sts-0", "StatefulSet", "api"),
		overProvisionedPod("prod", "api-dep-0", "Deployment", "api"),
		overProvisionedPod("prod", "web-0", "Deployment", "web"),
	)
	if err := l.Append(rec); err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	first, err := l.ListWorkloads(workloadQuery{})
	if err != nil {
		t.Fatalf("ListWorkloads() error = %v", err)
	}
	for range 20 {
		again, err := l.ListWorkloads(workloadQuery{})
		if err != nil {
			t.Fatalf("ListWorkloads() error = %v", err)
		}
		for i := range first.Workloads {
			if again.Workloads[i] != first.Workloads[i] {
				t.Fatalf("order changed at %d: %+v then %+v", i, first.Workloads[i], again.Workloads[i])
			}
		}
	}

	if first.Workloads[0].Name != "api" || first.Workloads[1].Name != "api" {
		t.Fatalf("expected both api workloads first, got %+v", first.Workloads)
	}
	if first.Workloads[0].Kind != "Deployment" || first.Workloads[1].Kind != "StatefulSet" {
		t.Errorf("kinds = %q then %q, want Deployment then StatefulSet",
			first.Workloads[0].Kind, first.Workloads[1].Kind)
	}
}

func TestPagingCoversCollidingNamesExactlyOnce(t *testing.T) {
	l := newLedger()
	rec := workloadRecord(t,
		overProvisionedPod("prod", "a-sts", "StatefulSet", "api"),
		overProvisionedPod("prod", "a-dep", "Deployment", "api"),
		overProvisionedPod("prod", "b-ds", "DaemonSet", "api"),
	)
	if err := l.Append(rec); err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	seen := map[string]int{}
	offset := 0
	for {
		page, err := l.ListWorkloads(workloadQuery{Offset: offset, Limit: 1})
		if err != nil {
			t.Fatalf("ListWorkloads() error = %v", err)
		}
		for _, w := range page.Workloads {
			seen[w.Kind+"/"+w.Name]++
		}
		if page.NextOffset == nil {
			break
		}
		offset = *page.NextOffset
	}

	if len(seen) != 3 {
		t.Fatalf("saw %d distinct workloads, want 3: %v", len(seen), seen)
	}
	for k, n := range seen {
		if n != 1 {
			t.Errorf("%s appeared %d times, want once", k, n)
		}
	}
}
