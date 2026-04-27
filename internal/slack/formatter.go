package slack

import (
	"fmt"
	"strings"

	"github.com/tanrikuluozlem/burn/internal/advisor"
	"github.com/tanrikuluozlem/burn/internal/analyzer"
)

// FormatOptions configures report formatting
type FormatOptions struct {
	ShowAllPods bool
}

func FormatCostReport(report *analyzer.CostReport) *Message {
	return FormatCostReportWithOptions(report, FormatOptions{})
}

func FormatCostReportWithOptions(report *analyzer.CostReport, opts FormatOptions) *Message {
	idlePercent := 0.0
	if report.MonthlyCost > 0 {
		idlePercent = (report.TotalIdleCost / report.MonthlyCost) * 100
	}

	// Header with summary
	summaryText := fmt.Sprintf("💰 *Monthly:* $%.0f | *Idle:* $%.0f (%.0f%%)\n📦 *Nodes:* %d | *Pods:* %d",
		report.MonthlyCost, report.TotalIdleCost, idlePercent,
		report.TotalNodes, report.TotalPods)

	blocks := []Block{
		{
			Type: "header",
			Text: &TextObject{
				Type: "plain_text",
				Text: costReportHeader(report.Period),
			},
		},
		{
			Type: "section",
			Text: &TextObject{
				Type: "mrkdwn",
				Text: summaryText,
			},
		},
	}

	// Nodes section
	if len(report.Nodes) > 0 {
		nodeLines := make([]string, 0, len(report.Nodes))
		for _, n := range report.Nodes {
			spot := ""
			if n.IsSpot {
				spot = " spot"
			}
			nodeLines = append(nodeLines, fmt.Sprintf("• `%s` %s%s - $%.0f/mo (%.0f%% idle)",
				truncate(n.Name, 35), n.InstanceType, spot, n.MonthlyPrice, n.IdlePercent*100))
		}

		blocks = append(blocks, Block{
			Type: "section",
			Text: &TextObject{
				Type: "mrkdwn",
				Text: "*Nodes:*\n" + strings.Join(nodeLines, "\n"),
			},
		})
	}

	hasPrometheus := report.MetricsSource == "prometheus"

	if len(report.Namespaces) > 0 {
		nsLines := make([]string, 0, len(report.Namespaces))
		var allocated float64
		for _, ns := range report.Namespaces {
			allocated += ns.MonthlyCost
			if hasPrometheus {
				nsLines = append(nsLines, fmt.Sprintf("• `%s` — %d pods — $%.0f/mo\n    CPU: %s req → %s used | MEM: %s req → %s used",
					ns.Name, ns.PodCount, ns.MonthlyCost,
					formatMillicores(ns.CPURequest), formatCores(ns.CPUUsage),
					formatBytes(ns.MemRequest), formatBytes(ns.MemUsage)))
			} else {
				nsLines = append(nsLines, fmt.Sprintf("• `%s` — %d pods — $%.0f/mo",
					ns.Name, ns.PodCount, ns.MonthlyCost))
			}
		}
		idle := report.MonthlyCost - allocated
		if idle > 0 {
			nsLines = append(nsLines, fmt.Sprintf("• _Idle (unallocated)_ — $%.0f/mo", idle))
		}
		nsLines = append(nsLines, fmt.Sprintf("*Total: $%.0f/mo*", report.MonthlyCost))
		blocks = append(blocks, Block{
			Type: "section",
			Text: &TextObject{
				Type: "mrkdwn",
				Text: "*Cost by Namespace:*\n" + strings.Join(nsLines, "\n"),
			},
		})
	} else if !hasPrometheus {
		blocks = append(blocks, Block{
			Type: "section",
			Text: &TextObject{
				Type: "mrkdwn",
				Text: "_💡 Add --prometheus for usage data_",
			},
		})
	}

	// Potential savings
	if report.WasteAnalysis.PotentialSavings > 0 {
		blocks = append(blocks, Block{
			Type: "section",
			Text: &TextObject{
				Type: "mrkdwn",
				Text: fmt.Sprintf("*Potential savings:* $%.0f/mo", report.WasteAnalysis.PotentialSavings),
			},
		})
	}

	return &Message{Blocks: blocks}
}

// sortPodsByWaste sorts pods by waste amount (highest waste first)
func sortPodsByWaste(pods []analyzer.PodEfficiency) []analyzer.PodEfficiency {
	sorted := make([]analyzer.PodEfficiency, len(pods))
	copy(sorted, pods)

	for i := 0; i < len(sorted)-1; i++ {
		for j := 0; j < len(sorted)-i-1; j++ {
			avgEffJ := (sorted[j].CPUEfficiency + sorted[j].MemEfficiency) / 2
			avgEffJ1 := (sorted[j+1].CPUEfficiency + sorted[j+1].MemEfficiency) / 2
			wasteJ := sorted[j].MonthlyCost * (1 - avgEffJ)
			wasteJ1 := sorted[j+1].MonthlyCost * (1 - avgEffJ1)

			if wasteJ < wasteJ1 {
				sorted[j], sorted[j+1] = sorted[j+1], sorted[j]
			}
		}
	}
	return sorted
}

func FormatAIReport(report *advisor.Report) *Message {
	blocks := []Block{
		{
			Type: "header",
			Text: &TextObject{
				Type: "plain_text",
				Text: "Recommendations",
			},
		},
		{
			Type: "section",
			Text: &TextObject{
				Type: "mrkdwn",
				Text: report.Summary,
			},
		},
	}

	for i, rec := range report.Recommendations {
		severity := severityEmoji(rec.Severity)
		recText := fmt.Sprintf("%s *%d. %s*\n%s", severity, i+1, rec.Title, rec.Description)
		if rec.Action != "" {
			recText += fmt.Sprintf("\n`%s`", rec.Action)
		}
		if rec.EstimatedSavings > 0 {
			recText += fmt.Sprintf("\n💰 Save $%.0f/mo", rec.EstimatedSavings)
		}

		blocks = append(blocks, Block{
			Type: "section",
			Text: &TextObject{
				Type: "mrkdwn",
				Text: recText,
			},
		})
	}

	if report.TotalPotentialSavings > 0 {
		blocks = append(blocks, Block{
			Type: "section",
			Text: &TextObject{
				Type: "mrkdwn",
				Text: fmt.Sprintf("*Total potential savings: $%.0f/mo*", report.TotalPotentialSavings),
			},
		})
	}

	return &Message{Blocks: blocks}
}

func FormatQuickCost(report *analyzer.CostReport) *Message {
	metricsNote := "(based on requests)"
	if report.MetricsSource == "prometheus" {
		metricsNote = "(based on actual usage)"
	}

	idlePercent := 0.0
	if report.MonthlyCost > 0 {
		idlePercent = (report.TotalIdleCost / report.MonthlyCost) * 100
	}

	text := fmt.Sprintf("*Cluster Cost Summary* %s\n"+
		"Nodes: %d | Pods: %d\n"+
		"Monthly: $%.2f | Idle: $%.2f (%.0f%%)",
		metricsNote,
		report.TotalNodes, report.TotalPods,
		report.MonthlyCost, report.TotalIdleCost, idlePercent)

	if report.WasteAnalysis.PotentialSavings > 0 {
		text += fmt.Sprintf("\n_Potential savings: $%.2f/mo_", report.WasteAnalysis.PotentialSavings)
	}

	return &Message{
		Blocks: []Block{
			{
				Type: "section",
				Text: &TextObject{
					Type: "mrkdwn",
					Text: text,
				},
			},
		},
	}
}

func severityEmoji(severity advisor.Severity) string {
	switch strings.ToLower(string(severity)) {
	case "critical":
		return ":red_circle:"
	case "high":
		return ":large_orange_circle:"
	case "medium":
		return ":large_yellow_circle:"
	default:
		return ":white_circle:"
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

func costReportHeader(period string) string {
	if period != "" {
		return fmt.Sprintf("Kubernetes Cost Report (%s avg)", period)
	}
	return "Kubernetes Cost Report"
}

func formatCores(cores float64) string {
	m := cores * 1000
	if m < 1 {
		return "<1m"
	}
	if m >= 1000 {
		return fmt.Sprintf("%.1f", cores)
	}
	return fmt.Sprintf("%.0fm", m)
}

func formatMillicores(m int64) string {
	if m >= 1000 {
		return fmt.Sprintf("%.1f", float64(m)/1000)
	}
	return fmt.Sprintf("%dm", m)
}

func formatBytes(b int64) string {
	const (
		gi = 1024 * 1024 * 1024
		mi = 1024 * 1024
	)
	if b >= gi {
		return fmt.Sprintf("%.1fGi", float64(b)/float64(gi))
	}
	return fmt.Sprintf("%dMi", b/mi)
}

