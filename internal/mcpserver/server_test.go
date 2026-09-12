package mcpserver

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tanrikuluozlem/burn/internal/analyzer"
	"github.com/tanrikuluozlem/burn/internal/billing"
)

func TestServerInstructions(t *testing.T) {
	if ServerInstructions == "" {
		t.Fatal("ServerInstructions must be non-empty")
	}
	checks := []struct {
		substr string
	}{
		{"do not sum them"},
		{"not guaranteed realizable savings"},
		{"Do not invent rankings"},
		{"Reserved Instance vs Savings Plan"},
		{"investigation before deletion"},
	}
	for _, c := range checks {
		if !strings.Contains(ServerInstructions, c.substr) {
			t.Errorf("ServerInstructions missing %q", c.substr)
		}
	}
}

func TestToolDescriptionGrounding(t *testing.T) {
	checks := []struct {
		desc   string
		substr string
	}{
		{AnalyzeDescription, "do not sum them"},
		{AnalyzeDescription, "not guaranteed realizable savings"},
		{AnalyzeDescription, "does not guarantee node removal"},
		{AnalyzeDescription, "Do not invent priority rankings"},
		{SpotDescription, "do not sum across categories"},
		{ReconcileDescription, "authoritative"},
		{ReconcileDescription, "Preserve RI/SP/Spot terminology"},
		{ReconcileDescription, "investigation before deletion"},
	}

	for _, c := range checks {
		if !strings.Contains(c.desc, c.substr) {
			t.Errorf("description missing %q", c.substr)
		}
	}
}

func TestNamespaceResultFound(t *testing.T) {
	report := &analyzer.CostReport{
		Namespaces: []analyzer.NamespaceCost{
			{Name: "app", PodCount: 5, CPURequest: 2000, CPUUsage: 0.5, MemRequest: 4 * 1024 * 1024 * 1024, MemUsage: 1024 * 1024 * 1024, MonthlyCost: 200},
			{Name: "system", PodCount: 3, MonthlyCost: 80},
		},
	}

	result, _, err := namespaceResult(report, "app")
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Error("expected success for existing namespace")
	}

	text := result.Content[0].(*mcp.TextContent).Text
	var ns mcpNamespace
	if err := json.Unmarshal([]byte(text), &ns); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ns.Name != "app" {
		t.Errorf("name = %s, want app", ns.Name)
	}
	if ns.MonthlyCost != 200 {
		t.Errorf("cost = %.2f, want 200", ns.MonthlyCost)
	}
	if ns.CPUCoresReq != 2.0 {
		t.Errorf("cpu_cores_requested = %.2f, want 2.0 (2000m converted)", ns.CPUCoresReq)
	}
	if ns.CPUCoresUsed != 0.5 {
		t.Errorf("cpu_cores_used = %.2f, want 0.5", ns.CPUCoresUsed)
	}
	if ns.MemBytesReq != 4*1024*1024*1024 {
		t.Errorf("mem_bytes_requested = %d, want 4GiB", ns.MemBytesReq)
	}
	if ns.MemBytesUsed != 1024*1024*1024 {
		t.Errorf("mem_bytes_used = %d, want 1GiB", ns.MemBytesUsed)
	}
}

func TestNamespaceResultNotFound(t *testing.T) {
	report := &analyzer.CostReport{
		Namespaces: []analyzer.NamespaceCost{
			{Name: "app", PodCount: 5, MonthlyCost: 200},
		},
	}

	result, _, err := namespaceResult(report, "nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Error("expected error for missing namespace")
	}
}

func TestSpotResultEmpty(t *testing.T) {
	report := &analyzer.CostReport{}

	result, _, err := spotResult(report)
	if err != nil {
		t.Fatal(err)
	}

	var data struct {
		ReadyCount    int     `json:"ready_count"`
		NotReadyCount int     `json:"not_ready_count"`
		Total         int     `json:"total"`
		Savings       float64 `json:"potential_savings_monthly"`
	}
	text := result.Content[0].(*mcp.TextContent).Text
	if err := json.Unmarshal([]byte(text), &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Total != 0 {
		t.Errorf("total = %d, want 0", data.Total)
	}
	if data.Savings != 0 {
		t.Errorf("savings = %.2f, want 0", data.Savings)
	}
}

func TestSpotResultWithWorkloads(t *testing.T) {
	report := &analyzer.CostReport{
		SpotReadiness: []analyzer.SpotReadiness{
			{Name: "nginx", Namespace: "app", Kind: "Deployment", Replicas: 2, Status: "spot-ready", MonthlyCost: 50},
			{Name: "redis", Namespace: "data", Kind: "StatefulSet", Replicas: 1, Status: "not-ready", Reason: "StatefulSet — risk of data loss"},
			{Name: "agent", Namespace: "system", Kind: "DaemonSet", Replicas: 3, Status: "not-ready", Reason: "DaemonSet — runs on every node"},
		},
		SpotSavings: 32.50,
	}

	result, _, err := spotResult(report)
	if err != nil {
		t.Fatal(err)
	}

	var data struct {
		ReadyCount    int            `json:"ready_count"`
		NotReadyCount int            `json:"not_ready_count"`
		Total         int            `json:"total"`
		Savings       float64        `json:"potential_savings_monthly"`
		Blockers      map[string]int `json:"blockers"`
	}
	text := result.Content[0].(*mcp.TextContent).Text
	if err := json.Unmarshal([]byte(text), &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.ReadyCount != 1 {
		t.Errorf("ready = %d, want 1", data.ReadyCount)
	}
	if data.NotReadyCount != 2 {
		t.Errorf("not_ready = %d, want 2", data.NotReadyCount)
	}
	if data.Total != 3 {
		t.Errorf("total = %d, want 3", data.Total)
	}
	if data.Blockers["statefulset"] != 1 {
		t.Errorf("statefulset blocker = %d, want 1", data.Blockers["statefulset"])
	}
	if data.Blockers["daemonset"] != 1 {
		t.Errorf("daemonset blocker = %d, want 1", data.Blockers["daemonset"])
	}
	if data.Savings != 32.50 {
		t.Errorf("savings = %.2f, want 32.50", data.Savings)
	}
}

func TestAnalyzeResultIdleNote(t *testing.T) {
	report := &analyzer.CostReport{
		TotalNodes:    2,
		TotalPods:     5,
		MonthlyCost:   300,
		TotalIdleCost: 111,
		Nodes: []analyzer.NodeCost{
			{Name: "node-1", MonthlyPrice: 150, IdlePercent: 0.37, IdleCostMonthly: 55.5},
			{Name: "node-2", MonthlyPrice: 150, IdlePercent: 0.37, IdleCostMonthly: 55.5},
		},
	}

	result, _, err := analyzeResult(report)
	if err != nil {
		t.Fatal(err)
	}

	text := result.Content[0].(*mcp.TextContent).Text

	var parsed struct {
		TotalIdleCost float64 `json:"total_idle_cost"`
		IdlePercent   float64 `json:"idle_percent"`
		IdleNote      string  `json:"idle_note"`
		MonthlyCost   float64 `json:"monthly_cost"`
	}
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if parsed.IdleNote == "" {
		t.Fatal("idle_note field is missing")
	}
	if !strings.Contains(parsed.IdleNote, "not guaranteed recoverable savings") {
		t.Errorf("idle_note should mention non-recoverable savings, got: %s", parsed.IdleNote)
	}
	if !strings.Contains(parsed.IdleNote, "scheduling feasibility") {
		t.Errorf("idle_note should mention scheduling feasibility, got: %s", parsed.IdleNote)
	}
	if parsed.TotalIdleCost != 111 {
		t.Errorf("total_idle_cost = %.2f, want 111", parsed.TotalIdleCost)
	}
	if parsed.MonthlyCost != 300 {
		t.Errorf("monthly_cost = %.2f, want 300", parsed.MonthlyCost)
	}
}

func TestReconcileEnvelopeNotes(t *testing.T) {
	report := &billing.ReconciliationReport{
		TotalEstimatedCost: 328.72,
		TotalActualCost:    412.17,
		TotalDifference:    83.45,
		TotalDiffPercent:   25.4,
		DiscountNote:       "coverage_gaps[].potential_saving is a forward-looking modeled RI pricing opportunity.",
		InfraCost: &billing.InfrastructureSummary{
			ComputeEstimated: 307.91,
			ComputeActual:    310.02,
			ManagementFee:    73.00,
			TotalEstimated:   328.72,
			TotalActual:      412.17,
		},
		OrphanedDisks: []billing.DiskReconciliation{
			{DiskName: "vol-001", ActualCost: 1.87, IsOrphaned: true},
		},
	}

	data, err := json.Marshal(struct {
		*billing.ReconciliationReport
		ReconcileNote    string `json:"reconcile_note"`
		OrphanedDiskNote string `json:"orphaned_disk_note"`
	}{
		ReconciliationReport: report,
		ReconcileNote:        reconcileNote,
		OrphanedDiskNote:     unmatchedDiskNote,
	})
	if err != nil {
		t.Fatal(err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	rn, ok := parsed["reconcile_note"].(string)
	if !ok || rn == "" {
		t.Fatal("reconcile_note field is missing")
	}
	if !strings.Contains(rn, "total_estimated_cost") {
		t.Error("reconcile_note should reference total_estimated_cost")
	}
	if !strings.Contains(rn, "total_actual_cost") {
		t.Error("reconcile_note should reference total_actual_cost")
	}
	if !strings.Contains(rn, "same category") {
		t.Error("reconcile_note should warn about cross-category comparison")
	}
	if !strings.Contains(rn, "management_fee") {
		t.Error("reconcile_note should address management_fee semantics")
	}

	dn, ok := parsed["orphaned_disk_note"].(string)
	if !ok || dn == "" {
		t.Fatal("orphaned_disk_note field is missing")
	}
	if !strings.Contains(dn, "no matching current Kubernetes PVC") {
		t.Error("orphaned_disk_note should explain what orphaned means")
	}
	if !strings.Contains(dn, "does not prove") {
		t.Error("orphaned_disk_note should state evidence boundary")
	}

	if parsed["total_estimated_cost"].(float64) != 328.72 {
		t.Errorf("total_estimated_cost changed")
	}
	if parsed["total_actual_cost"].(float64) != 412.17 {
		t.Errorf("total_actual_cost changed")
	}
	if parsed["total_difference"].(float64) != 83.45 {
		t.Errorf("total_difference changed")
	}

	if !strings.Contains(parsed["discount_note"].(string), "forward-looking modeled") {
		t.Error("discount_note should be preserved")
	}

	od, ok := parsed["orphaned_disks"].([]any)
	if !ok || len(od) != 1 {
		t.Errorf("orphaned_disks field should be present with 1 entry, got %v", parsed["orphaned_disks"])
	}

	ic := parsed["infra_cost"].(map[string]any)
	if ic["compute_estimated"].(float64) != 307.91 {
		t.Error("compute_estimated changed")
	}
	if ic["management_fee"].(float64) != 73.00 {
		t.Error("management_fee changed")
	}
}

func TestSpotDescriptionEligibilityBoundary(t *testing.T) {
	if !strings.Contains(SpotDescription, "technical eligibility") {
		t.Error("SpotDescription should mention technical eligibility")
	}
	if !strings.Contains(SpotDescription, "does not establish business criticality") {
		t.Error("SpotDescription should state it does not establish business criticality")
	}
}
