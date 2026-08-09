package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/tanrikuluozlem/burn/internal/advisor"
)

func captureOutput(f func()) string {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	f()

	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	io.Copy(&buf, r)
	return buf.String()
}

func TestOutputAIReport_SeparateOpportunities(t *testing.T) {
	report := &advisor.Report{
		Summary:              "Test summary",
		RightSizingSavings:   171.51,
		SpotSavings:          40.53,
		ConsolidationSavings: 28.76,
		Recommendations: []advisor.Recommendation{
			{Title: "Test", Description: "desc", Severity: advisor.SeverityLow},
		},
	}

	out := captureOutput(func() { outputAIReport(report) })

	if strings.Contains(out, "Largest optimization opportunity") {
		t.Error("must not contain 'Largest optimization opportunity'")
	}
	if !strings.Contains(out, "Rightsizing allocation opportunity: $171.51/mo") {
		t.Errorf("missing rightsizing line, got:\n%s", out)
	}
	if !strings.Contains(out, "Spot pricing opportunity: $40.53/mo") {
		t.Errorf("missing spot line, got:\n%s", out)
	}
	if !strings.Contains(out, "Consolidation opportunity: $28.76/mo") {
		t.Errorf("missing consolidation line, got:\n%s", out)
	}
}

func TestOutputAIReport_OnlyApplicableShown(t *testing.T) {
	report := &advisor.Report{
		Summary:            "Test",
		SpotSavings:        40.0,
		RightSizingSavings: 0,
		Recommendations: []advisor.Recommendation{
			{Title: "T", Description: "d", Severity: advisor.SeverityLow},
		},
	}

	out := captureOutput(func() { outputAIReport(report) })

	if !strings.Contains(out, "Spot pricing opportunity") {
		t.Error("missing spot line")
	}
	if strings.Contains(out, "Rightsizing") {
		t.Error("should not show rightsizing when zero")
	}
	if strings.Contains(out, "Consolidation") {
		t.Error("should not show consolidation when zero")
	}
}

func TestOutputAIReport_NoOpportunities(t *testing.T) {
	report := &advisor.Report{
		Summary: "All good",
		Recommendations: []advisor.Recommendation{
			{Title: "T", Description: "d", Severity: advisor.SeverityLow},
		},
	}

	out := captureOutput(func() { outputAIReport(report) })

	if strings.Contains(out, "opportunity") {
		t.Errorf("should not show any opportunity lines, got:\n%s", out)
	}
}
