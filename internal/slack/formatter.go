package slack

import (
	"fmt"
	"strings"

	"github.com/tanrikuluozlem/burn/internal/advisor"
	"github.com/tanrikuluozlem/burn/internal/analyzer"
)

func FormatCostReport(report *analyzer.CostReport) *Message {
	metricsNote := "_Based on pod requests_"
	if report.MetricsSource == "prometheus" {
		metricsNote = "_Based on Prometheus metrics (actual usage)_"
	}

	blocks := []Block{
		{
			Type: "header",
			Text: &TextObject{
				Type: "plain_text",
				Text: "Daily Kubernetes Cost Report",
			},
		},
		{
			Type: "section",
			Text: &TextObject{
				Type: "mrkdwn",
				Text: metricsNote,
			},
		},
		{
			Type: "section",
			Fields: []TextObject{
				{Type: "mrkdwn", Text: fmt.Sprintf("*Nodes:* %d", report.TotalNodes)},
				{Type: "mrkdwn", Text: fmt.Sprintf("*Pods:* %d", report.TotalPods)},
				{Type: "mrkdwn", Text: fmt.Sprintf("*Hourly:* $%.4f", report.HourlyCost)},
				{Type: "mrkdwn", Text: fmt.Sprintf("*Monthly:* $%.2f", report.MonthlyCost)},
			},
		},
	}

	if len(report.Nodes) > 0 {
		nodeLines := make([]string, 0, len(report.Nodes))
		for _, n := range report.Nodes {
			spot := ""
			if n.IsSpot {
				spot = " (spot)"
			}
			nodeLines = append(nodeLines, fmt.Sprintf("• `%s` %s%s - %.0f%% util - $%.2f/mo",
				truncate(n.Name, 25), n.InstanceType, spot, n.Utilization*100, n.MonthlyPrice))
		}

		blocks = append(blocks, Block{
			Type: "section",
			Text: &TextObject{
				Type: "mrkdwn",
				Text: "*Nodes:*\n" + strings.Join(nodeLines, "\n"),
			},
		})
	}

	if len(report.WasteAnalysis.UnderutilizedNodes) > 0 {
		blocks = append(blocks, Block{
			Type: "divider",
		})

		wasteLines := []string{
			fmt.Sprintf("*Potential Monthly Savings:* $%.2f", report.WasteAnalysis.PotentialSavings),
			"",
		}
		for _, u := range report.WasteAnalysis.UnderutilizedNodes {
			wasteLines = append(wasteLines, fmt.Sprintf("• `%s` (%.0f%% util): %s",
				truncate(u.Name, 25), u.Utilization*100, u.Recommendation))
		}

		blocks = append(blocks, Block{
			Type: "section",
			Text: &TextObject{
				Type: "mrkdwn",
				Text: "*Waste Analysis:*\n" + strings.Join(wasteLines, "\n"),
			},
		})
	}

	return &Message{Blocks: blocks}
}

func FormatAIReport(report *advisor.Report) *Message {
	blocks := []Block{
		{
			Type: "header",
			Text: &TextObject{
				Type: "plain_text",
				Text: "AI Cost Optimization Recommendations",
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

	if report.TotalPotentialSavings > 0 {
		blocks = append(blocks, Block{
			Type: "section",
			Text: &TextObject{
				Type: "mrkdwn",
				Text: fmt.Sprintf("*Total Potential Savings:* $%.2f/month", report.TotalPotentialSavings),
			},
		})
	}

	for i, rec := range report.Recommendations {
		severity := severityEmoji(rec.Severity)
		recText := fmt.Sprintf("%s *%d. %s*\n%s", severity, i+1, rec.Title, rec.Description)
		if rec.Action != "" {
			recText += fmt.Sprintf("\n→ %s", rec.Action)
		}
		if rec.EstimatedSavings > 0 {
			recText += fmt.Sprintf("\n_~$%.0f/month_", rec.EstimatedSavings)
		}

		blocks = append(blocks, Block{
			Type: "section",
			Text: &TextObject{
				Type: "mrkdwn",
				Text: recText,
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

	text := fmt.Sprintf("*Cluster Cost Summary* %s\n"+
		"Nodes: %d | Pods: %d\n"+
		"Hourly: $%.4f | Monthly: $%.2f",
		metricsNote,
		report.TotalNodes, report.TotalPods,
		report.HourlyCost, report.MonthlyCost)

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
	switch severity {
	case advisor.SeverityCritical:
		return ":red_circle:"
	case advisor.SeverityHigh:
		return ":large_orange_circle:"
	case advisor.SeverityMedium:
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
