package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"text/tabwriter"
	"time"

	"github.com/tanrikuluozlem/burn/internal/advisor"
	"github.com/tanrikuluozlem/burn/internal/analyzer"
	"github.com/tanrikuluozlem/burn/internal/collector"
	"github.com/tanrikuluozlem/burn/internal/pricing"
	"github.com/tanrikuluozlem/burn/internal/slack"
	"github.com/spf13/cobra"
)

var (
	namespace     string
	kubeconfig    string
	prometheusURL string
	output        string
	withAI        bool
	verbose       bool
	sendToSlack   bool
	slackWebhook  string
)

var analyzeCmd = &cobra.Command{
	Use:   "analyze",
	Short: "Analyze cluster costs and resource usage",
	RunE:  runAnalyze,
}

func init() {
	f := analyzeCmd.Flags()
	f.StringVarP(&namespace, "namespace", "n", "", "target namespace (default: all)")
	f.StringVar(&kubeconfig, "kubeconfig", "", "path to kubeconfig file")
	f.StringVar(&prometheusURL, "prometheus", "", "Prometheus server URL for usage metrics")
	f.StringVarP(&output, "output", "o", "table", "output format (table|json)")
	f.BoolVar(&withAI, "ai", false, "get AI-powered recommendations")
	f.BoolVarP(&verbose, "verbose", "v", false, "verbose output")
	f.BoolVar(&sendToSlack, "slack", false, "send report to Slack")
	f.StringVar(&slackWebhook, "slack-webhook", "", "Slack webhook URL (or set SLACK_WEBHOOK_URL)")

	rootCmd.AddCommand(analyzeCmd)
}

func runAnalyze(cmd *cobra.Command, _ []string) error {
	if verbose {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
			Level: slog.LevelDebug,
		})))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	coll, err := collector.New(kubeconfig, prometheusURL)
	if err != nil {
		return err
	}

	pp, err := pricing.NewCloudPricingProvider(ctx)
	if err != nil {
		return err
	}

	info, err := coll.Collect(ctx)
	if err != nil {
		return err
	}

	report, err := analyzer.New(pp).Analyze(ctx, info)
	if err != nil {
		return err
	}

	switch output {
	case "json":
		return outputJSON(report)
	default:
		outputTable(report)
	}

	if !withAI {
		return nil
	}

	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return fmt.Errorf("ANTHROPIC_API_KEY not set")
	}

	fmt.Println("\nfetching recommendations...")
	aiReport, err := advisor.New(apiKey).Analyze(ctx, report)
	if err != nil {
		return err
	}
	outputAIReport(aiReport)

	// Send to Slack if requested
	if sendToSlack {
		webhook := slackWebhook
		if webhook == "" {
			webhook = os.Getenv("SLACK_WEBHOOK_URL")
		}
		if webhook == "" {
			return fmt.Errorf("--slack requires --slack-webhook or SLACK_WEBHOOK_URL env var")
		}

		sc := slack.NewWebhookClient(webhook)
		if err := sc.Send(ctx, slack.FormatCostReport(report)); err != nil {
			return fmt.Errorf("failed to send cost report to Slack: %w", err)
		}
		if err := sc.Send(ctx, slack.FormatAIReport(aiReport)); err != nil {
			return fmt.Errorf("failed to send AI report to Slack: %w", err)
		}
		fmt.Println("\n✓ Report sent to Slack")
	}

	return nil
}

func outputJSON(report *analyzer.CostReport) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}

func outputTable(report *analyzer.CostReport) {
	fmt.Printf("\nCluster Cost Analysis - %s\n", report.GeneratedAt.Format(time.RFC3339))
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	fmt.Printf("\nNodes: %d | Pods: %d", report.TotalNodes, report.TotalPods)
	if report.SkippedNodes > 0 {
		fmt.Printf(" | Skipped: %d", report.SkippedNodes)
	}
	fmt.Printf("\nHourly: $%.4f | Monthly: $%.2f\n\n", report.HourlyCost, report.MonthlyCost)

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NODE\tTYPE\tSPOT\tPODS\tCPU%\tMEM%\tHOURLY\tMONTHLY")
	fmt.Fprintln(w, "────\t────\t────\t────\t────\t────\t──────\t───────")

	for _, n := range report.Nodes {
		spot := "no"
		if n.IsSpot {
			spot = "yes"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%.0f%%\t%.0f%%\t$%.4f\t$%.2f\n",
			truncate(n.Name, 20), n.InstanceType, spot, n.PodCount,
			n.CPURequested*100, n.MemRequested*100, n.HourlyPrice, n.MonthlyPrice)
	}
	w.Flush()

	if len(report.WasteAnalysis.UnderutilizedNodes) > 0 {
		fmt.Printf("\nWaste Analysis:\n")
		fmt.Printf("  Underutilized: %d nodes\n", len(report.WasteAnalysis.UnderutilizedNodes))
		fmt.Printf("  Potential savings: $%.2f/mo\n\n", report.WasteAnalysis.PotentialSavings)

		for _, u := range report.WasteAnalysis.UnderutilizedNodes {
			fmt.Printf("  - %s (%.0f%%): %s\n", u.Name, u.Utilization*100, u.Recommendation)
		}
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

func outputAIReport(report *advisor.Report) {
	fmt.Println("\nAI Recommendations")
	fmt.Println("------------------")
	fmt.Printf("%s\n\n", report.Summary)
	fmt.Printf("Potential savings: $%.2f/month\n\n", report.TotalPotentialSavings)

	for i, rec := range report.Recommendations {
		fmt.Printf("%d. [%s] %s\n", i+1, rec.Severity, rec.Title)
		fmt.Printf("   %s\n", rec.Description)
		if rec.Action != "" {
			fmt.Printf("   → %s\n", rec.Action)
		}
		if rec.EstimatedSavings > 0 {
			fmt.Printf("   ~$%.0f/month\n", rec.EstimatedSavings)
		}
		fmt.Println()
	}
}
