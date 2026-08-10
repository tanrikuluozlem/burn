package slack

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/tanrikuluozlem/burn/internal/advisor"
	"github.com/tanrikuluozlem/burn/internal/analyzer"
	"github.com/tanrikuluozlem/burn/internal/output"
)

const maxBlockText = 2900

func FormatCostReport(report *analyzer.CostReport, suppressLegacySavings bool) *Message {
	idlePercent := 0.0
	if report.MonthlyCost > 0 {
		idlePercent = (report.TotalIdleCost / report.MonthlyCost) * 100
	}

	// Header with summary
	summaryText := fmt.Sprintf("💰 *Monthly:* $%.2f | *Idle:* $%.2f (%.0f%%)\n📦 *Nodes:* %d | *Pods:* %d",
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
			nodeLines = append(nodeLines, fmt.Sprintf("• `%s` %s%s - $%.2f/mo (%.0f%% idle)",
				output.Truncate(n.Name, 35), n.InstanceType, spot, n.MonthlyPrice, n.IdlePercent*100))
		}

		blocks = append(blocks, splitSectionBlocks("*Nodes:*\n"+strings.Join(nodeLines, "\n"))...)
	}

	hasPrometheus := report.MetricsSource == "prometheus"

	if len(report.Namespaces) > 0 {
		nsLines := make([]string, 0, len(report.Namespaces))
		for _, ns := range report.Namespaces {
			if hasPrometheus {
				nsLines = append(nsLines, fmt.Sprintf("• `%s` — %d pods — $%.2f/mo\n    CPU: %s req → %s used | MEM: %s req → %s used",
					ns.Name, ns.PodCount, ns.MonthlyCost,
					output.FormatMillicores(ns.CPURequest), output.FormatCores(ns.CPUUsage),
					output.FormatBytes(ns.MemRequest), output.FormatBytes(ns.MemUsage)))
			} else {
				nsLines = append(nsLines, fmt.Sprintf("• `%s` — %d pods — $%.2f/mo",
					ns.Name, ns.PodCount, ns.MonthlyCost))
			}
		}
		if report.TotalIdleCost > 0 {
			nsLines = append(nsLines, fmt.Sprintf("• _Idle (unallocated)_ — $%.2f/mo", report.TotalIdleCost))
		}
		nsLines = append(nsLines, fmt.Sprintf("*Total: $%.2f/mo*", report.MonthlyCost))
		blocks = append(blocks, splitSectionBlocks("*Cost by Namespace:*\n"+strings.Join(nsLines, "\n"))...)
	} else if !hasPrometheus {
		blocks = append(blocks, Block{
			Type: "section",
			Text: &TextObject{
				Type: "mrkdwn",
				Text: "_💡 Add --prometheus for usage data_",
			},
		})
	}

	// Storage costs
	if len(report.PVCosts) > 0 {
		pvLines := make([]string, 0, len(report.PVCosts))
		for _, pv := range report.PVCosts {
			pvLines = append(pvLines, fmt.Sprintf("• `%s` (%s) — %s %.0fGi — $%.2f/mo",
				pv.Name, pv.Namespace, pv.StorageClass, pv.CapacityGiB, pv.MonthlyCost))
		}
		blocks = append(blocks, splitSectionBlocks("*Storage:*\n"+strings.Join(pvLines, "\n"))...)
	}

	// LB costs
	if len(report.LBCosts) > 0 {
		lbLines := make([]string, 0, len(report.LBCosts))
		for _, lb := range report.LBCosts {
			lbLines = append(lbLines, fmt.Sprintf("• `%s` (%s) — $%.2f/mo", lb.Name, lb.Namespace, lb.MonthlyCost))
		}
		blocks = append(blocks, splitSectionBlocks("*Load Balancers:*\n"+strings.Join(lbLines, "\n"))...)
	}

	// Spot readiness summary
	if len(report.SpotReadiness) > 0 {
		ready := 0
		var discount float64
		var source string
		for _, s := range report.SpotReadiness {
			if s.Status == "spot-ready" {
				ready++
				if discount == 0 {
					discount = s.Discount
					source = s.PricingSource
				}
			}
		}
		total := len(report.SpotReadiness)
		spotText := fmt.Sprintf("*Spot Readiness:* %d/%d workloads spot-ready", ready, total)
		if report.SpotSavings > 0 {
			sourceLabel := "estimate"
			if source == "api" {
				sourceLabel = "real-time"
			} else if source == "advisor" {
				sourceLabel = "Spot Advisor"
			}
			spotText += fmt.Sprintf(" — save $%.2f/mo (%.0f%% discount, %s)", report.SpotSavings, discount*100, sourceLabel)
		}
		blocks = append(blocks, Block{
			Type: "section",
			Text: &TextObject{
				Type: "mrkdwn",
				Text: spotText,
			},
		})
	}

	// Cost breakdown
	blocks = append(blocks, Block{
		Type: "section",
		Text: &TextObject{
			Type: "mrkdwn",
			Text: fmt.Sprintf("*Cost Breakdown:*\nCompute: $%.2f | Storage: $%.2f | LB: $%.2f | Network: $%.2f\n*Total: $%.2f/mo*",
				report.MonthlyCost, report.TotalPVCost, report.TotalLBCost, report.TotalNetworkCost, report.TotalMonthlyCost),
		},
	})

	if report.WasteAnalysis.PotentialSavings > 0 && !suppressLegacySavings {
		blocks = append(blocks, Block{
			Type: "section",
			Text: &TextObject{
				Type: "mrkdwn",
				Text: fmt.Sprintf("*Potential savings:* $%.2f/mo", report.WasteAnalysis.PotentialSavings),
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
				Text: "Recommendations",
			},
		},
	}
	blocks = append(blocks, splitSectionBlocks(report.Summary)...)

	for i, rec := range report.Recommendations {
		severity := severityEmoji(rec.Severity)
		recText := fmt.Sprintf("%s *%d. %s*\n%s", severity, i+1, rec.Title, rec.Description)
		if rec.Action != "" {
			recText += fmt.Sprintf("\n`%s`", rec.Action)
		}
		if rec.EstimatedSavings > 0 {
			recText += fmt.Sprintf("\n💰 Save $%.2f/mo", rec.EstimatedSavings)
		}

		blocks = append(blocks, Block{
			Type: "section",
			Text: &TextObject{
				Type: "mrkdwn",
				Text: recText,
			},
		})
	}

	var savingsLines []string
	if report.RightSizingSavings > 0 {
		savingsLines = append(savingsLines, fmt.Sprintf("Rightsizing allocation opportunity: $%.2f/mo", report.RightSizingSavings))
	}
	if report.SpotSavings > 0 {
		savingsLines = append(savingsLines, fmt.Sprintf("Spot pricing opportunity: $%.2f/mo", report.SpotSavings))
	}
	if report.ConsolidationSavings > 0 {
		savingsLines = append(savingsLines, fmt.Sprintf("Consolidation opportunity: $%.2f/mo", report.ConsolidationSavings))
	}
	if len(savingsLines) > 0 {
		blocks = append(blocks, Block{
			Type: "section",
			Text: &TextObject{
				Type: "mrkdwn",
				Text: "*" + strings.Join(savingsLines, "\n") + "*",
			},
		})
	}

	return &Message{Blocks: blocks}
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

func SplitText(text string) []string {
	var chunks []string
	remaining := text
	for len(remaining) > 0 {
		chunk := remaining
		if len(chunk) > maxBlockText {
			cut := strings.LastIndex(chunk[:maxBlockText], "\n")
			if cut <= 0 {
				cut = maxBlockText
				for cut > 0 && !utf8.RuneStart(remaining[cut]) {
					cut--
				}
				if cut == 0 {
					cut = maxBlockText
				}
			}
			chunk = remaining[:cut]
			remaining = remaining[cut:]
		} else {
			remaining = ""
		}
		chunks = append(chunks, chunk)
	}
	return chunks
}

func splitSectionBlocks(text string) []Block {
	var blocks []Block
	for _, chunk := range SplitText(text) {
		blocks = append(blocks, Block{
			Type: "section",
			Text: &TextObject{
				Type: "mrkdwn",
				Text: chunk,
			},
		})
	}
	return blocks
}

func costReportHeader(period string) string {
	if period != "" {
		return fmt.Sprintf("Kubernetes Cost Report (%s avg)", period)
	}
	return "Kubernetes Cost Report"
}
