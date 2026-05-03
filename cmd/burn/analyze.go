package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"text/tabwriter"
	"time"

	"github.com/tanrikuluozlem/burn/internal/advisor"
	"github.com/tanrikuluozlem/burn/internal/analyzer"
	"github.com/tanrikuluozlem/burn/internal/collector"
	"github.com/tanrikuluozlem/burn/internal/pricing"
	"github.com/tanrikuluozlem/burn/internal/slack"
	"github.com/spf13/cobra"
)

var validPeriod = regexp.MustCompile(`^\d{1,4}[smhdwy]$`)

func isValidPeriod(p string) bool {
	return validPeriod.MatchString(p)
}

var (
	namespace     string
	kubeconfig    string
	kubecontext   string
	prometheusURL string
	output        string
	withAI        bool
	verbose       bool
	sendToSlack   bool
	slackWebhook  string
	showAllPods   bool
	period        string
	cpuPrice      float64
	ramPrice      float64
	gpuPrice      float64
	storagePrice  float64
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
	f.StringVar(&kubecontext, "context", "", "kubeconfig context to use")
	f.StringVar(&prometheusURL, "prometheus", "", "Prometheus server URL for usage metrics")
	f.StringVarP(&output, "output", "o", "table", "output format (table|json)")
	f.BoolVar(&withAI, "ai", false, "get AI-powered recommendations")
	f.BoolVarP(&verbose, "verbose", "v", false, "verbose output")
	f.BoolVar(&sendToSlack, "slack", false, "send report to Slack")
	f.StringVar(&slackWebhook, "slack-webhook", "", "Slack webhook URL (or set SLACK_WEBHOOK_URL)")
	f.BoolVar(&showAllPods, "all", false, "show all pods (default: top 5 wasteful)")
	f.StringVar(&period, "period", "", "analysis period (e.g. 1h, 7d, 30d)")
	f.Float64Var(&cpuPrice, "cpu-price", 0, "custom CPU price per core per hour (on-prem)")
	f.Float64Var(&ramPrice, "ram-price", 0, "custom RAM price per GiB per hour (on-prem)")
	f.Float64Var(&gpuPrice, "gpu-price", 0, "custom GPU price per unit per hour (on-prem)")
	f.Float64Var(&storagePrice, "storage-price", 0, "custom storage price per GiB per month (on-prem)")

	rootCmd.AddCommand(analyzeCmd)
}

func runAnalyze(cmd *cobra.Command, _ []string) error {
	if verbose {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
			Level: slog.LevelDebug,
		})))
	}

	if period != "" && !isValidPeriod(period) {
		return fmt.Errorf("invalid period %q: use Prometheus duration format (e.g. 1h, 7d, 30d)", period)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	coll, err := collector.New(kubeconfig, kubecontext, namespace, prometheusURL, period)
	if err != nil {
		return err
	}

	pp, err := pricing.NewCloudPricingProvider(ctx)
	if err != nil {
		return err
	}

	// Custom pricing for on-prem nodes
	if cpuPrice > 0 || ramPrice > 0 || gpuPrice > 0 || storagePrice > 0 {
		cp := &pricing.CustomPricing{
			CPUCostPerCoreHr:    cpuPrice,
			RAMCostPerGiBHr:     ramPrice,
			GPUCostPerHr:        gpuPrice,
			StoragePricePerGiBMo: storagePrice,
		}
		if cp.CPUCostPerCoreHr == 0 {
			cp.CPUCostPerCoreHr = pricing.DefaultCPUCostPerCoreHr
		}
		if cp.RAMCostPerGiBHr == 0 {
			cp.RAMCostPerGiBHr = pricing.DefaultRAMCostPerGiBHr
		}
		pp.SetCustomPricing(cp)
	}

	info, err := coll.Collect(ctx)
	if err != nil {
		return err
	}

	report, err := analyzer.New(pp).Analyze(ctx, info)
	if err != nil {
		return err
	}
	report.Period = period

	switch output {
	case "json":
		return outputJSON(report)
	default:
		outputTable(report)
	}

	// On-prem pricing notice
	if cpuPrice == 0 && ramPrice == 0 {
		hasOnPrem := false
		hasGPU := false
		for _, node := range info.Nodes {
			if node.CloudProvider == collector.CloudUnknown {
				hasOnPrem = true
			}
			if node.GPUCount > 0 && gpuPrice == 0 {
				hasGPU = true
			}
		}
		if hasOnPrem {
			fmt.Println("\nℹ On-prem nodes detected. For accurate costs, set your rates:")
			fmt.Println("  --cpu-price <$/core/hr> --ram-price <$/GiB/hr>")
			fmt.Println("  Without custom pricing, cloud-equivalent rates are used.")
		}
		if hasGPU {
			fmt.Println("\nℹ GPU detected but price unknown. GPU cost excluded.")
			fmt.Println("  Set GPU price: --gpu-price <$/GPU/hr>")
		}
	}

	// Get AI recommendations if requested
	var aiReport *advisor.Report
	if withAI {
		apiKey := os.Getenv("ANTHROPIC_API_KEY")
		if apiKey == "" {
			return fmt.Errorf("ANTHROPIC_API_KEY not set")
		}

		fmt.Println("\nfetching recommendations...")
		var err error
		aiReport, err = advisor.New(apiKey).Analyze(ctx, report)
		if err != nil {
			return err
		}
		outputAIReport(aiReport)
	}

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
		opts := slack.FormatOptions{ShowAllPods: showAllPods}
		if err := sc.Send(ctx, slack.FormatCostReportWithOptions(report, opts)); err != nil {
			return fmt.Errorf("failed to send cost report to Slack: %w", err)
		}
		// Send AI report only if we have one
		if aiReport != nil {
			if err := sc.Send(ctx, slack.FormatAIReport(aiReport)); err != nil {
				return fmt.Errorf("failed to send AI report to Slack: %w", err)
			}
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
	hasPrometheus := report.MetricsSource == "prometheus"

	// Namespace detail view
	if namespace != "" {
		fmt.Println()
		if len(report.AllPods) > 0 {
			outputNamespacePods(report.AllPods, hasPrometheus)
		} else {
			fmt.Printf("No pods found in namespace %s\n", namespace)
		}
		return
	}

	// Cluster-wide view
	idlePercent := 0.0
	if report.MonthlyCost > 0 {
		idlePercent = (report.TotalIdleCost / report.MonthlyCost) * 100
	}

	fmt.Println()
	header := "Kubernetes Cost Report"
	if report.Period != "" {
		header += fmt.Sprintf(" (%s avg)", report.Period)
	}
	fmt.Println(header)
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Printf("Monthly: $%.0f | Idle: $%.0f (%.0f%%)\n", report.MonthlyCost, report.TotalIdleCost, idlePercent)
	fmt.Printf("Nodes: %d | Pods: %d\n", report.TotalNodes, report.TotalPods)

	fmt.Println("\nNODES")
	fmt.Println("─────")
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tTYPE\tSPOT\tCOST/MO\tIDLE%")

	for _, n := range report.Nodes {
		spot := ""
		if n.IsSpot {
			spot = "yes"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t$%.0f\t%.0f%%\n",
			truncate(n.Name, 40), n.InstanceType, spot, n.MonthlyPrice, n.IdlePercent*100)
	}
	w.Flush()

	if len(report.Namespaces) > 0 {
		outputNamespaceSummary(report.Namespaces, hasPrometheus, report.MonthlyCost)
	}

	if len(report.PVCosts) > 0 {
		fmt.Println("\nSTORAGE")
		fmt.Println("───────")
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tNAMESPACE\tCLASS\tSIZE\tCOST/MO")
		for _, pv := range report.PVCosts {
			fmt.Fprintf(w, "%s\t%s\t%s\t%.0fGi\t$%.0f\n",
				truncate(pv.Name, 30), truncate(pv.Namespace, 15), pv.StorageClass, pv.CapacityGiB, pv.MonthlyCost)
		}
		w.Flush()
	}

	if len(report.LBCosts) > 0 {
		fmt.Println("\nLOAD BALANCERS")
		fmt.Println("──────────────")
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tNAMESPACE\tCOST/MO")
		for _, lb := range report.LBCosts {
			fmt.Fprintf(w, "%s\t%s\t$%.0f\n",
				truncate(lb.Name, 30), truncate(lb.Namespace, 15), lb.MonthlyCost)
		}
		w.Flush()
	}

	fmt.Println("\nCOST BREAKDOWN")
	fmt.Println("━━━━━━━━━━━━━━")
	fmt.Printf("Compute:         $%.0f\n", report.MonthlyCost)
	fmt.Printf("Storage:         $%.0f\n", report.TotalPVCost)
	fmt.Printf("Load Balancers:  $%.0f\n", report.TotalLBCost)
	fmt.Printf("Network:         $%.0f\n", report.TotalNetworkCost)
	fmt.Printf("Total:           $%.0f\n", report.TotalMonthlyCost)

	if report.WasteAnalysis.PotentialSavings > 0 {
		fmt.Printf("\nPotential savings: $%.0f/mo\n", report.WasteAnalysis.PotentialSavings)
	}
}

func outputNamespaceSummary(namespaces []analyzer.NamespaceCost, hasPrometheus bool, monthlyCost float64) {
	fmt.Println("\nNAMESPACES")
	fmt.Println("──────────")
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	if hasPrometheus {
		fmt.Fprintln(w, "NAMESPACE\tPODS\tCPU REQ→USED\tMEM REQ→USED\tCOST/MO")
	} else {
		fmt.Fprintln(w, "NAMESPACE\tPODS\tCPU REQ\tMEM REQ\tCOST/MO")
	}

	var allocated float64
	for _, ns := range namespaces {
		allocated += ns.MonthlyCost
		if hasPrometheus {
			fmt.Fprintf(w, "%s\t%d\t%s → %s\t%s → %s\t$%.0f\n",
				truncate(ns.Name, 25),
				ns.PodCount,
				formatMillicores(ns.CPURequest), formatCores(ns.CPUUsage),
				formatBytes(ns.MemRequest), formatBytes(ns.MemUsage),
				ns.MonthlyCost)
		} else {
			fmt.Fprintf(w, "%s\t%d\t%s\t%s\t$%.0f\n",
				truncate(ns.Name, 25),
				ns.PodCount,
				formatMillicores(ns.CPURequest),
				formatBytes(ns.MemRequest),
				ns.MonthlyCost)
		}
	}

	idle := monthlyCost - allocated
	w.Flush()
	if idle > 0 {
		fmt.Printf("%-25s%s$%.0f\n", "Idle (unallocated)", "                              ", idle)
		fmt.Println("─────────────────────────────────────────────────────────")
		fmt.Printf("%-25s%s$%.0f\n", "Total", "                              ", monthlyCost)
	}
}

func outputNamespacePods(pods []analyzer.PodEfficiency, hasPrometheus bool) {
	ns := pods[0].Namespace
	var totalCost float64
	for _, p := range pods {
		totalCost += p.MonthlyCost
	}
	fmt.Printf("\nNAMESPACE: %s (%d pods, $%.0f/mo)\n", ns, len(pods), totalCost)
	fmt.Println("──────────────────────────────────")
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)

	if hasPrometheus {
		fmt.Fprintln(w, "POD\tCPU REQ→USED\tMEM REQ→USED\tCOST/MO")
		for _, p := range pods {
			fmt.Fprintf(w, "%s\t%s → %s\t%s → %s\t$%.0f\n",
				truncate(p.Name, 35),
				formatMillicores(p.CPURequest), formatCores(p.CPUUsage),
				formatBytes(p.MemRequest), formatBytes(p.MemUsage),
				p.MonthlyCost)
		}
	} else {
		fmt.Fprintln(w, "POD\tCPU REQ\tMEM REQ\tCOST/MO")
		for _, p := range pods {
			fmt.Fprintf(w, "%s\t%s\t%s\t$%.0f\n",
				truncate(p.Name, 35),
				formatMillicores(p.CPURequest),
				formatBytes(p.MemRequest),
				p.MonthlyCost)
		}
	}
	w.Flush()
}

func outputTopWastefulPods(pods []analyzer.PodEfficiency) {
	sorted := sortPodsByWaste(pods)

	fmt.Println("\nTOP OVER-PROVISIONED PODS")
	fmt.Println("─────────────────────────")
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAMESPACE\tPOD\tCPU REQ→USED\tMEM REQ→USED\tCOST/MO")

	maxPods := 5
	if showAllPods || len(sorted) <= maxPods {
		maxPods = len(sorted)
	}

	for i := 0; i < maxPods; i++ {
		p := sorted[i]

		fmt.Fprintf(w, "%s\t%s\t%s → %s\t%s → %s\t$%.0f\n",
			truncate(p.Namespace, 20),
			truncate(p.Name, 25),
			formatMillicores(p.CPURequest), formatCores(p.CPUUsage),
			formatBytes(p.MemRequest), formatBytes(p.MemUsage),
			p.MonthlyCost)
	}
	w.Flush()

	if len(pods) > maxPods && !showAllPods {
		fmt.Printf("\n...and %d more pods (use --all to show all)\n", len(pods)-maxPods)
	}
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


func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

func outputAIReport(report *advisor.Report) {
	fmt.Println("\nRECOMMENDATIONS")
	fmt.Println("───────────────")
	fmt.Printf("%s\n\n", report.Summary)

	for i, rec := range report.Recommendations {
		severityIcon := severityIcon(rec.Severity)
		fmt.Printf("%s %d. %s\n", severityIcon, i+1, rec.Title)
		fmt.Printf("   %s\n", rec.Description)
		if rec.Action != "" {
			fmt.Printf("   $ %s\n", rec.Action)
		}
		if rec.EstimatedSavings > 0 {
			fmt.Printf("   Save $%.0f/mo\n", rec.EstimatedSavings)
		}
		fmt.Println()
	}

	if report.TotalPotentialSavings > 0 {
		fmt.Printf("Total potential savings: $%.0f/mo\n", report.TotalPotentialSavings)
	}
}

func severityIcon(severity advisor.Severity) string {
	switch severity {
	case advisor.SeverityCritical:
		return "[!!!]"
	case advisor.SeverityHigh:
		return "[!!]"
	case advisor.SeverityMedium:
		return "[!]"
	default:
		return "[i]"
	}
}
