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
	ofmt "github.com/tanrikuluozlem/burn/internal/output"
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
	showSpot      bool
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
	f.BoolVar(&showSpot, "spot", false, "show spot instance readiness details")

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

	if p := os.Getenv("PROMETHEUS_URL"); p != "" {
		prometheusURL = p
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// When --ai is used with --namespace, collect full cluster data for AI
	collectNS := namespace
	if withAI && namespace != "" {
		collectNS = ""
	}

	coll, err := collector.New(kubeconfig, kubecontext, collectNS, prometheusURL, period)
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
	report.Period = coll.Period()

	// When --ai + --namespace: show namespace pods, AI gets full cluster
	if withAI && namespace != "" {
		var nsPods []analyzer.PodEfficiency
		for _, p := range report.AllPods {
			if p.Namespace == namespace {
				nsPods = append(nsPods, p)
			}
		}
		if len(nsPods) > 0 {
			var nsPVCs []analyzer.PVCost
			var nsPVCost float64
			for _, pv := range report.PVCosts {
				if pv.Namespace == namespace {
					nsPVCs = append(nsPVCs, pv)
					nsPVCost += pv.MonthlyCost
				}
			}
			outputNamespacePods(nsPods, report.MetricsSource == "prometheus", nsPVCs, nsPVCost)
		}
	}

	switch output {
	case "json":
		return outputJSON(report)
	default:
		if !(withAI && namespace != "") {
			outputTable(report)
		}
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
		aiReport, err = advisor.New(apiKey).Analyze(ctx, report, namespace)
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
		// Filter PVCs for this namespace
		var nsPVCost float64
		var nsPVCs []analyzer.PVCost
		for _, pv := range report.PVCosts {
			if pv.Namespace == namespace {
				nsPVCs = append(nsPVCs, pv)
				nsPVCost += pv.MonthlyCost
			}
		}
		if len(report.AllPods) > 0 {
			outputNamespacePods(report.AllPods, hasPrometheus, nsPVCs, nsPVCost)
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
	fmt.Printf("Monthly: $%.2f | Idle: $%.2f (%.0f%%)\n", report.MonthlyCost, report.TotalIdleCost, idlePercent)
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
		fmt.Fprintf(w, "%s\t%s\t%s\t$%.2f\t%.0f%%\n",
			ofmt.Truncate(n.Name, 40), n.InstanceType, spot, n.MonthlyPrice, n.IdlePercent*100)
	}
	w.Flush()

	if len(report.Namespaces) > 0 {
		outputNamespaceSummary(report.Namespaces, hasPrometheus, report.MonthlyCost, report.TotalIdleCost)
	}

	if len(report.PVCosts) > 0 {
		fmt.Println("\nSTORAGE")
		fmt.Println("───────")
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tNAMESPACE\tCLASS\tSIZE\tCOST/MO")
		for _, pv := range report.PVCosts {
			fmt.Fprintf(w, "%s\t%s\t%s\t%.0fGi\t$%.2f\n",
				ofmt.Truncate(pv.Name, 30), ofmt.Truncate(pv.Namespace, 15), pv.StorageClass, pv.CapacityGiB, pv.MonthlyCost)
		}
		w.Flush()
	}

	if len(report.LBCosts) > 0 {
		fmt.Println("\nLOAD BALANCERS")
		fmt.Println("──────────────")
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tNAMESPACE\tCOST/MO")
		for _, lb := range report.LBCosts {
			fmt.Fprintf(w, "%s\t%s\t$%.2f\n",
				ofmt.Truncate(lb.Name, 30), ofmt.Truncate(lb.Namespace, 15), lb.MonthlyCost)
		}
		w.Flush()
	}

	if len(report.SpotReadiness) > 0 {
		if showSpot {
			fmt.Println("\nSPOT READINESS")
			fmt.Println("──────────────")
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tNAMESPACE\tKIND\tREPLICAS\tSTATUS\tDISCOUNT\tINTERRUPT\tREASON")
			for _, s := range report.SpotReadiness {
				discount := "—"
				interrupt := "—"
				if s.Status == "spot-ready" {
					discount = fmt.Sprintf("%.0f%%", s.Discount*100)
					interrupt = interruptionLabel(s.InterruptionRate)
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\t%s\t%s\n",
					ofmt.Truncate(s.Name, 25), ofmt.Truncate(s.Namespace, 15), s.Kind, s.Replicas, s.Status, discount, interrupt, ofmt.Truncate(s.Reason, 40))
			}
			w.Flush()
			if report.SpotSavings > 0 {
				source := spotPricingSource(report.SpotReadiness)
				fmt.Printf("\nSpot savings: $%.2f/mo (%s)\n", report.SpotSavings, source)
			}
		} else {
			ready := 0
			for _, s := range report.SpotReadiness {
				if s.Status == "spot-ready" {
					ready++
				}
			}
			total := len(report.SpotReadiness)
			if report.SpotSavings > 0 {
				fmt.Printf("\nSpot-ready: %d/%d workloads, save $%.2f/mo (use --spot for details)\n", ready, total, report.SpotSavings)
			} else {
				fmt.Printf("\nSpot-ready: %d/%d workloads (use --spot for details)\n", ready, total)
			}
		}
	}

	fmt.Println("\nCOST BREAKDOWN")
	fmt.Println("━━━━━━━━━━━━━━")
	fmt.Printf("Compute:         $%.2f\n", report.MonthlyCost)
	fmt.Printf("Storage:         $%.2f\n", report.TotalPVCost)
	fmt.Printf("Load Balancers:  $%.2f\n", report.TotalLBCost)
	fmt.Printf("Network:         $%.2f\n", report.TotalNetworkCost)
	fmt.Printf("Total:           $%.2f\n", report.TotalMonthlyCost)

}

func outputNamespaceSummary(namespaces []analyzer.NamespaceCost, hasPrometheus bool, monthlyCost, idleCost float64) {
	fmt.Println("\nNAMESPACES")
	fmt.Println("──────────")
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	if hasPrometheus {
		fmt.Fprintln(w, "NAMESPACE\tPODS\tCPU REQ→USED\tMEM REQ→USED\tCOST/MO")
	} else {
		fmt.Fprintln(w, "NAMESPACE\tPODS\tCPU REQ\tMEM REQ\tCOST/MO")
	}

	for _, ns := range namespaces {
		if hasPrometheus {
			fmt.Fprintf(w, "%s\t%d\t%s → %s\t%s → %s\t$%.2f\n",
				ofmt.Truncate(ns.Name, 25),
				ns.PodCount,
				ofmt.FormatMillicores(ns.CPURequest), ofmt.FormatCores(ns.CPUUsage),
				ofmt.FormatBytes(ns.MemRequest), ofmt.FormatBytes(ns.MemUsage),
				ns.MonthlyCost)
		} else {
			fmt.Fprintf(w, "%s\t%d\t%s\t%s\t$%.2f\n",
				ofmt.Truncate(ns.Name, 25),
				ns.PodCount,
				ofmt.FormatMillicores(ns.CPURequest),
				ofmt.FormatBytes(ns.MemRequest),
				ns.MonthlyCost)
		}
	}

	w.Flush()
	if idleCost > 0 {
		fmt.Printf("%-25s%s$%.2f\n", "Idle (unallocated)", "                              ", idleCost)
		fmt.Println("─────────────────────────────────────────────────────────")
		fmt.Printf("%-25s%s$%.2f\n", "Total", "                              ", monthlyCost)
	}
}

func outputNamespacePods(pods []analyzer.PodEfficiency, hasPrometheus bool, pvcs []analyzer.PVCost, pvCost float64) {
	ns := pods[0].Namespace
	var computeCost float64
	for _, p := range pods {
		computeCost += p.MonthlyCost
	}
	totalCost := computeCost + pvCost
	fmt.Printf("\nNAMESPACE: %s (%d pods, $%.2f/mo)\n", ns, len(pods), totalCost)
	fmt.Println("──────────────────────────────────")
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)

	if hasPrometheus {
		fmt.Fprintln(w, "POD\tCPU REQ→USED\tMEM REQ→USED\tCOST/MO")
		for _, p := range pods {
			fmt.Fprintf(w, "%s\t%s → %s\t%s → %s\t$%.2f\n",
				ofmt.Truncate(p.Name, 35),
				ofmt.FormatMillicores(p.CPURequest), ofmt.FormatCores(p.CPUUsage),
				ofmt.FormatBytes(p.MemRequest), ofmt.FormatBytes(p.MemUsage),
				p.MonthlyCost)
		}
	} else {
		fmt.Fprintln(w, "POD\tCPU REQ\tMEM REQ\tCOST/MO")
		for _, p := range pods {
			fmt.Fprintf(w, "%s\t%s\t%s\t$%.2f\n",
				ofmt.Truncate(p.Name, 35),
				ofmt.FormatMillicores(p.CPURequest),
				ofmt.FormatBytes(p.MemRequest),
				p.MonthlyCost)
		}
	}
	w.Flush()

	if len(pvcs) > 0 {
		fmt.Println("\nSTORAGE")
		w = tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "PVC\tCLASS\tSIZE\tCOST/MO")
		for _, pv := range pvcs {
			fmt.Fprintf(w, "%s\t%s\t%.0fGi\t$%.2f\n",
				ofmt.Truncate(pv.Name, 30), pv.StorageClass, pv.CapacityGiB, pv.MonthlyCost)
		}
		w.Flush()
	}
}

func interruptionLabel(rate int) string {
	switch rate {
	case 0:
		return "<5%"
	case 1:
		return "5-10%"
	case 2:
		return "10-15%"
	case 3:
		return "15-20%"
	case 4:
		return ">20%"
	default:
		return "—"
	}
}

func spotPricingSource(results []analyzer.SpotReadiness) string {
	for _, r := range results {
		if r.Status == "spot-ready" {
			switch r.PricingSource {
			case "api":
				return "real-time API pricing"
			case "advisor":
				return "AWS Spot Advisor data"
			default:
				return "default estimate"
			}
		}
	}
	return "default estimate"
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
			fmt.Printf("   Save $%.2f/mo\n", rec.EstimatedSavings)
		}
		fmt.Println()
	}

	if report.TotalPotentialSavings > 0 {
		fmt.Printf("Total potential savings: $%.2f/mo\n", report.TotalPotentialSavings)
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
