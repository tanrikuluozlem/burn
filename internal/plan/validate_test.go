package plan

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

func record(tool, data string) Evidence {
	return Evidence{
		ID:   EvidenceID(tool, []byte(data)),
		Tool: tool,
		Data: json.RawMessage(data),
	}
}

// workloadsDoc holds three shapes: fully measured, measured but unpriceable,
// and not measured at all.
const workloadsDoc = `{"version":3,` +
	`"not_collected":["hpa","rollout_strategy"],` +
	`"workloads":[` +
	`{"kind":"Deployment","namespace":"prod","name":"api",` +
	`"current_cpu_millicores":2000,"current_mem_bytes":4294967296,` +
	`"candidate_cpu_millicores":450,"candidate_mem_bytes":2147483648,` +
	`"candidate_allocation_savings_monthly":127.4},` +
	`{"kind":"StatefulSet","namespace":"db","name":"pg",` +
	`"current_cpu_millicores":null,"current_mem_bytes":null,` +
	`"candidate_cpu_millicores":null,"candidate_mem_bytes":null,` +
	`"candidate_allocation_savings_monthly":null,` +
	`"blockers":["no pod-level recommendation"]},` +
	`{"kind":"Deployment","namespace":"prod","name":"web",` +
	`"current_cpu_millicores":1000,"current_mem_bytes":1073741824,` +
	`"candidate_cpu_millicores":300,"candidate_mem_bytes":null,` +
	`"candidate_allocation_savings_monthly":null,` +
	`"blockers":["cpu candidate savings withheld: not every scheduled pod was analyzed"]}]}`

func at(e Evidence, path string) EvidenceRef {
	return EvidenceRef{EvidenceID: e.ID, Path: path}
}

// validPlan returns a plan whose every number resolves against the ledger.
// Tests mutate one field at a time.
func validPlan() (*Plan, []Evidence) {
	workloads := record(WorkloadsTool, workloadsDoc)
	ledger := []Evidence{workloads}

	p := &Plan{
		SchemaVersion: SchemaVersion,
		ID:            "run-1",
		CreatedAt:     time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC),
		Goal:          "reduce cost without reliability risk",
		Readiness:     ReadinessInvestigation,
		Target:        Target{Kind: "Deployment", Namespace: "prod", Name: "api"},
		Current: Resources{
			CPUMillicores: &Value{Amount: 2000, Source: at(workloads, "workloads.0.current_cpu_millicores")},
			MemoryBytes:   &Value{Amount: 4294967296, Source: at(workloads, "workloads.0.current_mem_bytes")},
		},
		Proposed: Resources{
			CPUMillicores: &Value{Amount: 450, Source: at(workloads, "workloads.0.candidate_cpu_millicores")},
			MemoryBytes:   &Value{Amount: 2147483648, Source: at(workloads, "workloads.0.candidate_mem_bytes")},
		},
		ExpectedSavings: &Value{Amount: 127.4,
			Source: at(workloads, "workloads.0.candidate_allocation_savings_monthly")},
		Confidence:               "medium",
		UnsupportedPrerequisites: []string{"hpa", "rollout_strategy"},
	}
	return p, ledger
}

func TestValidateAcceptsTraceablePlan(t *testing.T) {
	p, ledger := validPlan()
	if err := Validate(p, ledger); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestValidateRejectsFabricatedSavings(t *testing.T) {
	p, ledger := validPlan()
	p.ExpectedSavings.Amount = 9999

	err := Validate(p, ledger)
	if err == nil {
		t.Fatal("expected a savings figure absent from evidence to be rejected")
	}
	if !strings.Contains(err.Error(), "expected_savings") {
		t.Errorf("error should name the failing field, got %v", err)
	}
}

func TestValidateRejectsFabricatedCurrentSpec(t *testing.T) {
	p, ledger := validPlan()
	p.Current.CPUMillicores.Amount = 8000

	if err := Validate(p, ledger); err == nil {
		t.Fatal("expected an invented current request to be rejected")
	}
}

func TestValidateRejectsUnknownEvidence(t *testing.T) {
	p, ledger := validPlan()
	p.Proposed.CPUMillicores.Source.EvidenceID = "ev-missing"

	err := Validate(p, ledger)
	if err == nil {
		t.Fatal("expected a reference to absent evidence to be rejected")
	}
	if !strings.Contains(err.Error(), "ev-missing") {
		t.Errorf("error should name the missing evidence, got %v", err)
	}
}

func TestValidateRejectsUncitedValue(t *testing.T) {
	p, ledger := validPlan()
	p.Proposed.MemoryBytes.Source = EvidenceRef{}

	if err := Validate(p, ledger); err == nil {
		t.Fatal("expected a number with no evidence to be rejected")
	}
}

func TestValidateRejectsClusterSavingsForWorkloadTarget(t *testing.T) {
	p, ledger := validPlan()
	cluster := record(SavingsTool, `{"total":900.0,"right_sizing":127.4}`)
	ledger = append(ledger, cluster)
	p.ExpectedSavings.Source = at(cluster, "right_sizing")

	err := Validate(p, ledger)
	if err == nil {
		t.Fatal("expected cluster-wide savings to be refused for a workload target")
	}
	if !strings.Contains(err.Error(), WorkloadsTool) {
		t.Errorf("error should name the required producer, got %v", err)
	}
}

// A number lifted from another workload resolves and matches; only the identity
// check catches it.
func TestValidateRejectsMismatchedWorkloadIdentity(t *testing.T) {
	p, ledger := validPlan()
	p.Target = Target{Kind: "StatefulSet", Namespace: "db", Name: "pg"}

	err := Validate(p, ledger)
	if err == nil {
		t.Fatal("expected evidence about another workload to be rejected")
	}
	if !strings.Contains(err.Error(), "pg") {
		t.Errorf("error should name the cited workload, got %v", err)
	}
}

func TestValidateRejectsCitationsSplitAcrossWorkloads(t *testing.T) {
	p, ledger := validPlan()
	workloads := ledger[0]
	p.Current.CPUMillicores = &Value{Amount: 2000, Source: at(workloads, "workloads.1.current_cpu_millicores")}

	if err := Validate(p, ledger); err == nil {
		t.Fatal("expected a plan drawing from two workloads to be rejected")
	}
}

func TestValidateRejectsValueFromTheWrongField(t *testing.T) {
	p, ledger := validPlan()
	p.Proposed.CPUMillicores.Source.Path = "workloads.0.current_cpu_millicores"
	p.Proposed.CPUMillicores.Amount = 2000

	err := Validate(p, ledger)
	if err == nil {
		t.Fatal("expected a proposal citing the current request to be rejected")
	}
	if !strings.Contains(err.Error(), "candidate_cpu_millicores") {
		t.Errorf("error should name the required field, got %v", err)
	}
}

func TestValidateRejectsUnsupportedEvidenceVersion(t *testing.T) {
	older := strings.Replace(workloadsDoc, `"version":3`, `"version":2`, 1)
	workloads := record(WorkloadsTool, older)

	p, _ := validPlan()
	for _, v := range []*Value{p.Current.CPUMillicores, p.Current.MemoryBytes,
		p.Proposed.CPUMillicores, p.Proposed.MemoryBytes, p.ExpectedSavings} {
		v.Source.EvidenceID = workloads.ID
	}

	err := Validate(p, []Evidence{workloads})
	if err == nil {
		t.Fatal("expected evidence from another schema version to be refused")
	}
	if !strings.Contains(err.Error(), "version") {
		t.Errorf("error should report the version mismatch, got %v", err)
	}
}

func TestValidateRejectsWorkloadIndexOutOfRange(t *testing.T) {
	p, ledger := validPlan()
	for _, v := range []*Value{p.Current.CPUMillicores, p.Current.MemoryBytes,
		p.Proposed.CPUMillicores, p.Proposed.MemoryBytes, p.ExpectedSavings} {
		v.Source.Path = strings.Replace(v.Source.Path, "workloads.0.", "workloads.9.", 1)
	}

	if err := Validate(p, ledger); err == nil {
		t.Fatal("expected an index past the recorded workloads to be rejected")
	}
}

func TestValidateRejectsDroppedBlockers(t *testing.T) {
	p, ledger := blockedPlan()
	p.Blockers = nil

	err := Validate(p, ledger)
	if err == nil {
		t.Fatal("expected a plan omitting the cited workload's blockers to be rejected")
	}
	if !strings.Contains(err.Error(), "blockers") {
		t.Errorf("error should name the field, got %v", err)
	}
}

func TestValidateAcceptsBlockedWorkloadWithItsBlockers(t *testing.T) {
	p, ledger := blockedPlan()

	if err := Validate(p, ledger); err != nil {
		t.Fatalf("a blocked workload should still be investigable: %v", err)
	}
}

// blockedPlan cites the workload whose savings were withheld.
func blockedPlan() (*Plan, []Evidence) {
	workloads := record(WorkloadsTool, workloadsDoc)

	p := &Plan{
		SchemaVersion: SchemaVersion,
		ID:            "run-3",
		CreatedAt:     time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC),
		Goal:          "find out why web cannot be priced",
		Readiness:     ReadinessInvestigation,
		Target:        Target{Kind: "Deployment", Namespace: "prod", Name: "web"},
		Current: Resources{
			CPUMillicores: &Value{Amount: 1000, Source: at(workloads, "workloads.2.current_cpu_millicores")},
		},
		Proposed: Resources{
			CPUMillicores: &Value{Amount: 300, Source: at(workloads, "workloads.2.candidate_cpu_millicores")},
		},
		Confidence:               "low",
		Blockers:                 []string{"cpu candidate savings withheld: not every scheduled pod was analyzed"},
		UnsupportedPrerequisites: []string{"hpa", "rollout_strategy"},
	}
	return p, []Evidence{workloads}
}

func TestValidateRejectsAlteredBlockers(t *testing.T) {
	p, ledger := validPlan()
	p.Blockers = []string{"none that matter"}

	err := Validate(p, ledger)
	if err == nil {
		t.Fatal("expected invented blockers to be rejected")
	}
	if !strings.Contains(err.Error(), "blockers") {
		t.Errorf("error should name the field, got %v", err)
	}
}

func TestValidateRejectsAlteredPrerequisites(t *testing.T) {
	p, ledger := validPlan()
	p.UnsupportedPrerequisites = []string{"hpa"}

	if err := Validate(p, ledger); err == nil {
		t.Fatal("expected a shortened prerequisite list to be rejected")
	}
}

func TestValidateRejectsExecutionReadiness(t *testing.T) {
	for _, readiness := range []string{"execution_eligible", "approved", ""} {
		p, ledger := validPlan()
		p.Readiness = readiness

		err := Validate(p, ledger)
		if err == nil {
			t.Fatalf("readiness %q: expected rejection", readiness)
		}
		if !strings.Contains(err.Error(), ReadinessInvestigation) {
			t.Errorf("readiness %q: error should name the only supported value, got %v", readiness, err)
		}
	}
}

func TestValidateAcceptsInvestigationWithoutSavings(t *testing.T) {
	workloads := record(WorkloadsTool, workloadsDoc)

	p := &Plan{
		SchemaVersion:            SchemaVersion,
		ID:                       "run-2",
		CreatedAt:                time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC),
		Goal:                     "find out why pg cannot be sized",
		Readiness:                ReadinessInvestigation,
		Target:                   Target{Kind: "Deployment", Namespace: "prod", Name: "api"},
		Current:                  Resources{CPUMillicores: &Value{Amount: 2000, Source: at(workloads, "workloads.0.current_cpu_millicores")}},
		Confidence:               "low",
		UnsupportedPrerequisites: []string{"hpa", "rollout_strategy"},
	}

	if err := Validate(p, []Evidence{workloads}); err != nil {
		t.Fatalf("an investigation with no savings should be valid: %v", err)
	}
}

func TestValidateRejectsPlanWithNoCitations(t *testing.T) {
	p, ledger := validPlan()
	p.Current = Resources{}
	p.Proposed = Resources{}
	p.ExpectedSavings = nil

	if err := Validate(p, ledger); err == nil {
		t.Fatal("expected a plan anchored to nothing to be rejected")
	}
}

func TestValidateRejectsNullEvidenceValue(t *testing.T) {
	p, ledger := validPlan()
	p.Target = Target{Kind: "StatefulSet", Namespace: "db", Name: "pg"}
	p.Current = Resources{}
	p.Proposed = Resources{}
	p.ExpectedSavings = &Value{Amount: 0,
		Source: at(ledger[0], "workloads.1.candidate_allocation_savings_monthly")}

	err := Validate(p, ledger)
	if err == nil {
		t.Fatal("expected a null evidence value to be rejected")
	}
	if !strings.Contains(err.Error(), "null") {
		t.Errorf("error should say the evidence holds nothing there, got %v", err)
	}
}

func TestValidateRejectsNonFiniteAmount(t *testing.T) {
	for _, amount := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		p, ledger := validPlan()
		p.ExpectedSavings.Amount = amount

		if err := Validate(p, ledger); err == nil {
			t.Errorf("amount %v: expected rejection", amount)
		}
	}
}

// A recomputed candidate will not reproduce the recorded value bit for bit.
func TestValidateToleratesFloatingPointNoise(t *testing.T) {
	p, ledger := validPlan()
	p.ExpectedSavings.Amount = 127.4 + 1e-12

	if err := Validate(p, ledger); err != nil {
		t.Fatalf("a difference below the tolerance should be accepted: %v", err)
	}

	p.ExpectedSavings.Amount = 127.5
	if err := Validate(p, ledger); err == nil {
		t.Fatal("a difference above the tolerance should be rejected")
	}
}

func TestValidateRejectsUnknownSchemaVersion(t *testing.T) {
	p, ledger := validPlan()
	p.SchemaVersion = SchemaVersion + 1

	if err := Validate(p, ledger); err == nil {
		t.Fatal("expected an unknown schema version to be refused, not migrated")
	}
}

func TestValidateRequiresTargetAndConfidence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Plan)
	}{
		{"missing id", func(p *Plan) { p.ID = "" }},
		{"missing goal", func(p *Plan) { p.Goal = "" }},
		{"missing target name", func(p *Plan) { p.Target.Name = "" }},
		{"invalid confidence", func(p *Plan) { p.Confidence = "very high" }},
	}

	for _, tt := range tests {
		p, ledger := validPlan()
		tt.mutate(p)
		if err := Validate(p, ledger); err == nil {
			t.Errorf("%s: expected rejection", tt.name)
		}
	}
}

// Without a boundary, ("ab","c") and ("a","bc") hash the same bytes.
func TestEvidenceIDSeparatesToolFromData(t *testing.T) {
	if EvidenceID("ab", []byte("c")) == EvidenceID("a", []byte("bc")) {
		t.Error("tool and data boundary is ambiguous")
	}
}

// A repeated id would index over the first record.
func TestValidateRejectsDuplicateEvidenceIDs(t *testing.T) {
	p, ledger := validPlan()
	ledger = append(ledger, ledger[0])

	err := Validate(p, ledger)
	if err == nil {
		t.Fatal("expected a duplicated ledger entry to be rejected")
	}
	if !strings.Contains(err.Error(), "twice") {
		t.Errorf("error should report the duplicate, got %v", err)
	}
}

func TestValidateRejectsTamperedEvidence(t *testing.T) {
	p, ledger := validPlan()
	ledger[0].Data = json.RawMessage(strings.Replace(workloadsDoc, "127.4", "9999", 1))
	p.ExpectedSavings.Amount = 9999

	err := Validate(p, ledger)
	if err == nil {
		t.Fatal("expected edited evidence to be rejected")
	}
	if !strings.Contains(err.Error(), "does not match its content") {
		t.Errorf("error should report the content mismatch, got %v", err)
	}
}

func TestResolveErrors(t *testing.T) {
	data := json.RawMessage(`{"pods":[{"name":"api","cpu_request":2000,"p95":null}],"total":12.5}`)

	tests := []struct {
		name string
		path string
	}{
		{"missing key", "pods.0.missing"},
		{"index out of range", "pods.5.cpu_request"},
		{"index into object", "pods.0.name.0"},
		{"not a number", "pods.0.name"},
		{"null value", "pods.0.p95"},
		{"empty path", ""},
	}

	for _, tt := range tests {
		if _, err := resolve(data, tt.path); err == nil {
			t.Errorf("%s: expected an error for path %q", tt.name, tt.path)
		}
	}

	got, err := resolve(data, "total")
	if err != nil || got != 12.5 {
		t.Errorf("resolve(total) = %v, %v; want 12.5, nil", got, err)
	}
}

func TestHashSurvivesRoundTrip(t *testing.T) {
	p, _ := validPlan()

	before, err := p.Hash()
	if err != nil {
		t.Fatalf("Hash() error = %v", err)
	}

	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var restored Plan
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	after, err := restored.Hash()
	if err != nil {
		t.Fatalf("Hash() error = %v", err)
	}
	if before != after {
		t.Errorf("hash changed across round trip: %s then %s", before, after)
	}
}

func TestHashChangesWithContent(t *testing.T) {
	p, _ := validPlan()
	before, _ := p.Hash()

	p.Proposed.CPUMillicores.Amount = 500
	after, _ := p.Hash()

	if before == after {
		t.Error("hash did not change after editing the proposed request")
	}
}

// A model must not be able to supply an executable string.
func TestPlanCarriesNoExecutableField(t *testing.T) {
	forbidden := []string{"command", "cmd", "exec", "script", "shell", "action"}

	seen := map[reflect.Type]bool{}
	var walk func(reflect.Type, string)
	walk = func(rt reflect.Type, path string) {
		for rt.Kind() == reflect.Ptr {
			rt = rt.Elem()
		}
		if rt.Kind() != reflect.Struct || rt == reflect.TypeOf(time.Time{}) || seen[rt] {
			return
		}
		seen[rt] = true

		for i := range rt.NumField() {
			field := rt.Field(i)
			name := strings.ToLower(field.Name)
			for _, word := range forbidden {
				if strings.Contains(name, word) {
					t.Errorf("%s%s may let a model supply an executable string", path, field.Name)
				}
			}
			walk(field.Type, path+field.Name+".")
		}
	}

	walk(reflect.TypeOf(Plan{}), "")
}
