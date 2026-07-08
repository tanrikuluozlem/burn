package mcpserver

import (
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tanrikuluozlem/burn/internal/analyzer"
)

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
		ReadyCount int     `json:"ready_count"`
		NotReadyCount int     `json:"not_ready_count"`
		Total      int     `json:"total"`
		Savings    float64 `json:"potential_savings_monthly"`
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
		ReadyCount int            `json:"ready_count"`
		NotReadyCount int            `json:"not_ready_count"`
		Total      int            `json:"total"`
		Savings    float64        `json:"potential_savings_monthly"`
		Blockers   map[string]int `json:"blockers"`
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
