package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"text/tabwriter"
	"time"

	"github.com/ozlemtanrikulu/burn/internal/advisor"
	"github.com/ozlemtanrikulu/burn/internal/analyzer"
	"github.com/ozlemtanrikulu/burn/internal/collector"
	"github.com/ozlemtanrikulu/burn/internal/pricing"
	"github.com/spf13/cobra"
)

var (
	namespace  string
	kubeconfig string
	output     string
	withAI     bool
	verbose    bool
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
	f.StringVarP(&output, "output", "o", "table", "output format (table|json)")
	f.BoolVar(&withAI, "ai", false, "get AI-powered recommendations")
	f.BoolVarP(&verbose, "verbose", "v", false, "verbose output")

	rootCmd.AddCommand(analyzeCmd)
}

func runAnalyze(cmd *cobra.Command, args []string) error {
	if verbose {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
			Level: slog.LevelDebug,
		})))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	coll, err := collector.New(kubeconfig)
	if err != nil {
		return fmt.Errorf("failed to create collector: %w", err)
	}

	pricingProvider, err := pricing.NewCloudPricingProvider(ctx)
	if err != nil {
		return fmt.Errorf("failed to create pricing provider: %w", err)
	}
	anal := analyzer.New(pricingProvider)

	// collect cluster data
	info, err := coll.Collect(ctx)
	if err != nil {
		return fmt.Errorf("failed to collect cluster info: %w", err)
	}

	// analyze costs
	report, err := anal.Analyze(ctx, info)
	if err != nil {
		return fmt.Errorf("failed to analyze costs: %w", err)
	}

	// output results
	switch output {
	case "json":
		if err := outputJSON(report); err != nil {
			return err
		}
	default:
		if err := outputTable(report); err != nil {
			return err
		}
	}

	// AI recommendations
	if withAI {
		apiKey := os.Getenv("ANTHROPIC_API_KEY")
		if apiKey == "" {
			return fmt.Errorf("ANTHROPIC_API_KEY environment variable is required for --ai flag")
		}

		fmt.Printf("\nFetching AI recommendations...\n")
		adv := advisor.New(apiKey)
		aiReport, err := adv.Analyze(ctx, report)
		if err != nil {
			return fmt.Errorf("failed to get AI recommendations: %w", err)
		}

		outputAIReport(aiReport)
	}

	return nil
}

func outputJSON(report *analyzer.CostReport) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}

func outputTable(report *analyzer.CostReport) error {
	fmt.Printf("\nCluster Cost Analysis - %s\n", report.GeneratedAt.Format(time.RFC3339))
	fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n\n")

	fmt.Printf("Summary:\n")
	fmt.Printf("  Nodes: %d | Pods: %d", report.TotalNodes, report.TotalPods)
	if report.SkippedNodes > 0 {
		fmt.Printf(" | Skipped: %d (see logs)", report.SkippedNodes)
	}
	fmt.Println()
	fmt.Printf("  Hourly Cost:  $%.4f\n", report.HourlyCost)
	fmt.Printf("  Monthly Cost: $%.2f\n\n", report.MonthlyCost)

	// node details
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NODE\tTYPE\tSPOT\tPODS\tCPU%\tMEM%\tHOURLY\tMONTHLY")
	fmt.Fprintln(w, "────\t────\t────\t────\t────\t────\t──────\t───────")

	for _, n := range report.Nodes {
		spot := "no"
		if n.IsSpot {
			spot = "yes"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%.0f%%\t%.0f%%\t$%.4f\t$%.2f\n",
			truncate(n.Name, 20),
			n.InstanceType,
			spot,
			n.PodCount,
			n.CPURequested*100,
			n.MemRequested*100,
			n.HourlyPrice,
			n.MonthlyPrice,
		)
	}
	w.Flush()

	// waste analysis
	if len(report.WasteAnalysis.UnderutilizedNodes) > 0 {
		fmt.Printf("\nWaste Analysis:\n")
		fmt.Printf("  Underutilized Nodes: %d\n", len(report.WasteAnalysis.UnderutilizedNodes))
		fmt.Printf("  Potential Monthly Savings: $%.2f\n\n", report.WasteAnalysis.PotentialSavings)

		for _, u := range report.WasteAnalysis.UnderutilizedNodes {
			fmt.Printf("  - %s (%.0f%% utilized): %s\n", u.Name, u.Utilization*100, u.Recommendation)
		}
	}

	return nil
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
