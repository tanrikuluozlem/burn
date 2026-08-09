package slack

import (
	"strings"
	"testing"

	"github.com/tanrikuluozlem/burn/internal/advisor"
	"github.com/tanrikuluozlem/burn/internal/analyzer"
	"github.com/tanrikuluozlem/burn/internal/output"
)

func TestFormatCostReport(t *testing.T) {
	report := &analyzer.CostReport{
		TotalNodes:    2,
		TotalPods:     10,
		HourlyCost:    0.50,
		MonthlyCost:   365.00,
		TotalIdleCost: 100.00,
		Nodes: []analyzer.NodeCost{
			{Name: "node-1", InstanceType: "t3.medium", IsSpot: true, IdlePercent: 0.25, IdleCostMonthly: 25, MonthlyPrice: 100},
			{Name: "node-2", InstanceType: "t3.large", IsSpot: false, IdlePercent: 0.70, IdleCostMonthly: 140, MonthlyPrice: 200},
		},
	}

	msg := FormatCostReport(report)

	if len(msg.Blocks) < 2 {
		t.Error("expected at least 2 blocks")
	}
	if msg.Blocks[0].Type != "header" {
		t.Error("first block should be header")
	}
}

func TestFormatCostReportWithWaste(t *testing.T) {
	report := &analyzer.CostReport{
		TotalNodes:    1,
		TotalPods:     5,
		HourlyCost:    0.10,
		MonthlyCost:   73.00,
		TotalIdleCost: 50.00,
		WasteAnalysis: analyzer.WasteAnalysis{
			PotentialSavings: 50.0,
			UnderutilizedNodes: []analyzer.UnderutilizedNode{
				{Name: "idle-node", IdlePercent: 0.95, Recommendation: "remove"},
			},
		},
	}

	msg := FormatCostReport(report)

	found := false
	for _, b := range msg.Blocks {
		if b.Text != nil && strings.Contains(b.Text.Text, "Potential savings") {
			found = true
		}
	}
	if !found {
		t.Error("expected potential savings block")
	}
}

func TestFormatAIReport(t *testing.T) {
	report := &advisor.Report{
		Summary:               "Cluster is over-provisioned",
		TotalPotentialSavings: 150.0,
		RightSizingSavings:    150.0,
		SpotSavings:           40.0,
		Recommendations: []advisor.Recommendation{
			{Title: "Downsize nodes", Description: "Use smaller instances", Severity: advisor.SeverityHigh},
		},
	}

	msg := FormatAIReport(report)

	if len(msg.Blocks) < 3 {
		t.Errorf("expected at least 3 blocks, got %d", len(msg.Blocks))
	}

	text := blocksText(msg.Blocks)
	if strings.Contains(text, "Largest optimization opportunity") {
		t.Error("must not contain 'Largest optimization opportunity'")
	}
	if !strings.Contains(text, "Rightsizing allocation opportunity") {
		t.Error("missing rightsizing opportunity line")
	}
	if !strings.Contains(text, "Spot pricing opportunity") {
		t.Error("missing spot opportunity line")
	}
}

func TestFormatAIReport_SeparateOpportunities(t *testing.T) {
	tests := []struct {
		name        string
		spot        float64
		consol      float64
		rightsiz    float64
		wantSpot    bool
		wantConsol  bool
		wantRight   bool
		wantLargest bool
	}{
		{
			name: "all three applicable",
			spot: 40, consol: 30, rightsiz: 170,
			wantSpot: true, wantConsol: true, wantRight: true,
		},
		{
			name:     "only spot",
			spot:     40,
			wantSpot: true,
		},
		{
			name:       "only consolidation",
			consol:     30,
			wantConsol: true,
		},
		{
			name:      "only rightsizing",
			rightsiz:  170,
			wantRight: true,
		},
		{
			name: "none applicable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			report := &advisor.Report{
				Summary:              "test",
				SpotSavings:          tt.spot,
				ConsolidationSavings: tt.consol,
				RightSizingSavings:   tt.rightsiz,
				Recommendations: []advisor.Recommendation{
					{Title: "Test", Description: "d", Severity: advisor.SeverityLow},
				},
			}
			msg := FormatAIReport(report)
			text := blocksText(msg.Blocks)

			if strings.Contains(text, "Largest optimization opportunity") {
				t.Error("must not contain 'Largest optimization opportunity'")
			}
			if tt.wantSpot != strings.Contains(text, "Spot pricing opportunity") {
				t.Errorf("Spot line presence = %v, want %v", !tt.wantSpot, tt.wantSpot)
			}
			if tt.wantConsol != strings.Contains(text, "Consolidation opportunity") {
				t.Errorf("Consolidation line presence = %v, want %v", !tt.wantConsol, tt.wantConsol)
			}
			if tt.wantRight != strings.Contains(text, "Rightsizing allocation opportunity") {
				t.Errorf("Rightsizing line presence = %v, want %v", !tt.wantRight, tt.wantRight)
			}
		})
	}
}

func blocksText(blocks []Block) string {
	var sb strings.Builder
	for _, b := range blocks {
		if b.Text != nil {
			sb.WriteString(b.Text.Text)
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

func TestSeverityEmoji(t *testing.T) {
	tests := []struct {
		sev  advisor.Severity
		want string
	}{
		{advisor.SeverityCritical, ":red_circle:"},
		{advisor.SeverityHigh, ":large_orange_circle:"},
		{advisor.SeverityMedium, ":large_yellow_circle:"},
		{advisor.SeverityLow, ":white_circle:"},
	}

	for _, tc := range tests {
		got := severityEmoji(tc.sev)
		if got != tc.want {
			t.Errorf("severityEmoji(%v) = %s, want %s", tc.sev, got, tc.want)
		}
	}
}

func TestSplitText_Short(t *testing.T) {
	chunks := SplitText("short text")
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if chunks[0] != "short text" {
		t.Errorf("unexpected text: %s", chunks[0])
	}
}

func TestSplitText_Boundary(t *testing.T) {
	line := strings.Repeat("x", 99) + "\n" // 100 char lines

	for _, size := range []int{2899, 2900, 2901, 6000} {
		var sb strings.Builder
		for sb.Len() < size {
			sb.WriteString(line)
		}
		input := sb.String()[:size]

		chunks := SplitText(input)

		if size <= 2900 && len(chunks) != 1 {
			t.Errorf("size %d: expected 1 chunk, got %d", size, len(chunks))
		}
		if size > 2900 && len(chunks) < 2 {
			t.Errorf("size %d: expected multiple chunks, got %d", size, len(chunks))
		}

		for i, c := range chunks {
			if len(c) > 2900 {
				t.Errorf("size %d: chunk %d exceeds 2900 bytes: %d", size, i, len(c))
			}
		}

		recombined := strings.Join(chunks, "")
		if recombined != input {
			t.Errorf("size %d: recombined chunks do not match original", size)
		}
	}
}

func TestSplitText_UTF8Turkish(t *testing.T) {
	// "ğüşıöç" = 12 bytes (6 two-byte runes)
	line := strings.Repeat("ğüşıöç", 200) + "\n" // 2401 bytes per line
	input := line + line                         // ~4802 bytes, forces split

	chunks := SplitText(input)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}

	var recombined strings.Builder
	for _, c := range chunks {
		if len(c) > 2900 {
			t.Errorf("chunk exceeds 2900 bytes: %d", len(c))
		}
		recombined.WriteString(c)
	}
	if recombined.String() != input {
		t.Error("recombined chunks do not match original")
	}
}

func TestSplitText_Emoji(t *testing.T) {
	// 🔥 = 4 bytes, no newlines — forces hard cut with multi-byte runes
	input := strings.Repeat("🔥", 1000) // 4000 bytes
	chunks := SplitText(input)

	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}

	var recombined strings.Builder
	for _, c := range chunks {
		if len(c) > 2900 {
			t.Errorf("chunk exceeds 2900 bytes: %d", len(c))
		}
		recombined.WriteString(c)
	}
	if recombined.String() != input {
		t.Error("recombined chunks do not match original")
	}
}

func TestSplitText_Empty(t *testing.T) {
	chunks := SplitText("")
	if len(chunks) != 0 {
		t.Fatalf("expected 0 chunks for empty input, got %d", len(chunks))
	}
}

func TestSplitSectionBlocks_WrapsCorrectly(t *testing.T) {
	input := "short text"
	blocks := splitSectionBlocks(input)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
	if blocks[0].Type != "section" {
		t.Errorf("expected section type, got %s", blocks[0].Type)
	}
	if blocks[0].Text.Type != "mrkdwn" {
		t.Errorf("expected mrkdwn type, got %s", blocks[0].Text.Type)
	}
	if blocks[0].Text.Text != input {
		t.Errorf("unexpected text: %s", blocks[0].Text.Text)
	}
}

func TestFormatCostReport_ManyNodes(t *testing.T) {
	nodes := make([]analyzer.NodeCost, 50)
	for i := range nodes {
		nodes[i] = analyzer.NodeCost{
			Name:         strings.Repeat("x", 35),
			InstanceType: "m5.2xlarge",
			MonthlyPrice: 100,
			IdlePercent:  0.30,
		}
	}
	report := &analyzer.CostReport{
		TotalNodes:  50,
		TotalPods:   500,
		MonthlyCost: 5000,
		Nodes:       nodes,
	}

	msg := FormatCostReport(report)
	for i, b := range msg.Blocks {
		if b.Text != nil && len(b.Text.Text) > 2900 {
			t.Errorf("block %d exceeds 2900 chars: %d", i, len(b.Text.Text))
		}
	}
}

func TestFormatAIReport_LongSummary(t *testing.T) {
	var sb strings.Builder
	line := strings.Repeat("analysis ", 11) + "\n" // ~100 chars per line
	for sb.Len() < 4000 {
		sb.WriteString(line)
	}

	report := &advisor.Report{
		Summary:               sb.String(),
		TotalPotentialSavings: 100.0,
		RightSizingSavings:    100.0,
		Recommendations: []advisor.Recommendation{
			{Title: "Test", Description: "desc", Severity: advisor.SeverityLow},
		},
	}

	msg := FormatAIReport(report)
	for i, b := range msg.Blocks {
		if b.Text != nil && len(b.Text.Text) > 2900 {
			t.Errorf("block %d exceeds 2900 chars: %d", i, len(b.Text.Text))
		}
	}
}

func TestTruncate(t *testing.T) {
	if output.Truncate("short", 10) != "short" {
		t.Error("should not truncate short strings")
	}
	if output.Truncate("this is a very long string", 10) != "this is..." {
		t.Error("should truncate with ellipsis")
	}
}
