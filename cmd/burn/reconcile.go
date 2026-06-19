package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"os"
	"text/tabwriter"
	"time"

	"github.com/tanrikuluozlem/burn/internal/advisor"
	"github.com/tanrikuluozlem/burn/internal/analyzer"
	"github.com/tanrikuluozlem/burn/internal/billing"
	"github.com/tanrikuluozlem/burn/internal/collector"
	ofmt "github.com/tanrikuluozlem/burn/internal/output"
	"github.com/tanrikuluozlem/burn/internal/pricing"
	"github.com/spf13/cobra"
)

var (
	curDatabase      string
	curTable         string
	curOutputLoc     string
	curWorkgroup     string
	curRegion        string
	curDays          int
	reconcileOut     string
	reconcileAI      bool
	reconcileSetup   bool
	reconcileProvider string
	azureSubscription string
	azureCostType     string
	dataDelayHours    int
)

var reconcileCmd = &cobra.Command{
	Use:   "reconcile",
	Short: "Compare estimated costs with actual cloud bill",
	RunE:  runReconcile,
}

func init() {
	f := reconcileCmd.Flags()
	f.StringVar(&curDatabase, "cur-database", "", "Athena database for CUR (or CUR_DATABASE env)")
	f.StringVar(&curTable, "cur-table", "", "Athena table for CUR (or CUR_TABLE env)")
	f.StringVar(&curOutputLoc, "cur-output", "", "S3 output for Athena results (or CUR_OUTPUT_LOCATION env)")
	f.StringVar(&curWorkgroup, "cur-workgroup", "", "Athena workgroup (or CUR_WORKGROUP env, default: primary)")
	f.StringVar(&curRegion, "cur-region", "", "AWS region for Athena (or CUR_REGION env)")
	f.IntVar(&curDays, "days", 7, "number of days to reconcile")
	f.StringVar(&kubeconfig, "kubeconfig", "", "path to kubeconfig file")
	f.StringVar(&kubecontext, "context", "", "kubeconfig context to use")
	f.StringVarP(&reconcileOut, "output", "o", "table", "output format (table|json)")
	f.BoolVar(&reconcileAI, "ai", false, "get AI analysis of reconciliation results")
	f.BoolVar(&reconcileSetup, "setup", false, "show setup instructions")
	f.StringVar(&reconcileProvider, "provider", "aws", "cloud provider (aws|azure)")
	f.StringVar(&azureSubscription, "azure-subscription", "", "Azure subscription ID (or AZURE_SUBSCRIPTION_ID env)")
	f.StringVar(&azureCostType, "cost-type", "amortized", "Azure cost type (amortized|actual)")
	f.IntVar(&dataDelayHours, "data-delay", 48, "billing data delay in hours (default 48, use 72 for Azure PAYG)")
	f.BoolVarP(&verbose, "verbose", "v", false, "verbose output")

	rootCmd.AddCommand(reconcileCmd)
}

func runReconcile(cmd *cobra.Command, _ []string) error {
	if reconcileSetup {
		printSetupGuide()
		return nil
	}

	if verbose {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
			Level: slog.LevelDebug,
		})))
	}

	if v := os.Getenv("CUR_DATABASE"); v != "" && curDatabase == "" {
		curDatabase = v
	}
	if v := os.Getenv("CUR_TABLE"); v != "" && curTable == "" {
		curTable = v
	}
	if v := os.Getenv("CUR_OUTPUT_LOCATION"); v != "" && curOutputLoc == "" {
		curOutputLoc = v
	}
	if v := os.Getenv("CUR_WORKGROUP"); v != "" && curWorkgroup == "" {
		curWorkgroup = v
	}
	if curWorkgroup == "" {
		curWorkgroup = "primary"
	}
	if v := os.Getenv("CUR_REGION"); v != "" && curRegion == "" {
		curRegion = v
	}

	if curDays < 1 {
		return fmt.Errorf("--days must be at least 1")
	}
	if curDays > 365 {
		return fmt.Errorf("--days must be at most 365")
	}

	timeout := time.Duration(curDays+5) * time.Minute
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	prometheusURL := os.Getenv("PROMETHEUS_URL")
	coll, err := collector.New(kubeconfig, kubecontext, "", prometheusURL, "")
	if err != nil {
		return err
	}

	pp, err := pricing.NewCloudPricingProvider(ctx)
	if err != nil {
		return err
	}

	fmt.Fprintln(os.Stderr, "Collecting cluster data...")
	info, err := coll.Collect(ctx)
	if err != nil {
		return err
	}

	report, err := analyzer.New(pp).Analyze(ctx, info)
	if err != nil {
		return err
	}

	dataDelay := time.Duration(dataDelayHours) * time.Hour
	end := time.Now().UTC().Add(-dataDelay)
	start := end.AddDate(0, 0, -curDays)

	var result *billing.ReconciliationReport

	switch reconcileProvider {
	case "azure":
		if azureSubscription == "" {
			azureSubscription = os.Getenv("AZURE_SUBSCRIPTION_ID")
		}
		azureCfg := billing.AzureConfig{SubscriptionID: azureSubscription, CostType: azureCostType}
		azureClient, err := billing.NewAzureCostClient(ctx, azureCfg)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Querying Azure Cost Management (%s to %s)...\n",
			start.Format("Jan 2"), end.Format("Jan 2, 2006"))
		estimatedCosts, pvEstimates, lbEstimates := billing.BuildEstimateMaps(report)
		result, err = billing.ReconcileAzure(ctx, azureClient, info.Nodes, estimatedCosts, report.Namespaces, info.PVCs, pvEstimates, info.LoadBalancers, lbEstimates, start, end, float64(curDays))
		if err != nil {
			return err
		}
	default:
		athenaCfg := billing.AthenaConfig{
			Database:       curDatabase,
			Table:          curTable,
			OutputLocation: curOutputLoc,
			WorkGroup:      curWorkgroup,
			Region:         curRegion,
		}
		athenaClient, err := billing.NewAthenaClient(ctx, athenaCfg)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Querying CUR via Athena (%s to %s)...\n",
			start.Format("Jan 2"), end.Format("Jan 2, 2006"))
		reconciler := billing.NewReconciler(athenaClient)
		result, err = reconciler.Reconcile(ctx, report, info, start, end)
		if err != nil {
			return err
		}
	}

	// Enrich coverage gaps with real RI pricing from cloud APIs
	billing.EnrichCoverageGaps(ctx, result.CoverageGaps, pp)

	switch reconcileOut {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	default:
		outputReconcileTable(result, reconcileProvider)
	}

	if reconcileAI {
		apiKey := os.Getenv("ANTHROPIC_API_KEY")
		if apiKey == "" {
			return fmt.Errorf("ANTHROPIC_API_KEY not set")
		}

		resultJSON, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal reconciliation report: %w", err)
		}
		source := "CUR"
		if reconcileProvider == "azure" {
			source = "Azure Cost Management"
		}
		question := fmt.Sprintf("Analyze this %s reconciliation report. Explain why the estimated vs actual costs differ. What discounts are applied? What actions should be taken?\n\n%s", source, string(resultJSON))

		fmt.Println("\nfetching AI analysis...")
		_, err = advisor.New(apiKey).AskStream(ctx, report, question, func(text string) {
			fmt.Print(text)
		})
		if err != nil {
			return err
		}
		fmt.Println()
		fmt.Println("\nAnalysis based on cluster data and actual billing.")
	}

	return nil
}

func fmtDiff(estimated, actual float64) string {
	diff := actual - estimated
	if math.Abs(diff) < 0.005 {
		return ""
	}
	if estimated == 0 || actual == 0 {
		return fmt.Sprintf("$%+.2f", diff)
	}
	pct := diff / estimated * 100
	if math.Round(pct) == 0 {
		return fmt.Sprintf("$%+.2f", diff)
	}
	return fmt.Sprintf("$%+.2f (%+.0f%%)", diff, pct)
}

func outputReconcileTable(r *billing.ReconciliationReport, provider string) {
	fmt.Println()
	title := "CUR Reconciliation"
	if provider == "azure" {
		title = "Azure Cost Reconciliation"
	}
	fmt.Printf("%s (%s - %s)\n",
		title, r.PeriodStart.Format("Jan 2"), r.PeriodEnd.Format("Jan 2, 2006"))
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Printf("Note: %s\n", r.DataDelay)

	if len(r.MissingCURColumns) > 0 {
		fmt.Printf("Missing CUR columns: %s (RI/SP data unavailable)\n",
			fmt.Sprintf("%v", r.MissingCURColumns))
	}

	fmt.Println("\nNODES")
	fmt.Println("─────")
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tTYPE\tPRICING\tEST/MO\tCOMPUTE\tTRANSFER\tACTUAL/MO\tDIFF")
	for _, n := range r.Nodes {
		diff := fmtDiff(n.EstimatedMonthlyCost, n.ActualCost)
		transfer := ""
		if n.ActualTransferCost > 0.01 {
			transfer = fmt.Sprintf("$%.2f", n.ActualTransferCost)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t$%.2f\t$%.2f\t%s\t$%.2f\t%s\n",
			ofmt.Truncate(n.NodeName, 25),
			n.InstanceType,
			n.PricingTerm,
			n.EstimatedMonthlyCost,
			n.ActualComputeCost,
			transfer,
			n.ActualCost,
			diff)
	}
	w.Flush()

	if r.TotalRISavings >= 0.005 || r.TotalSPSavings >= 0.005 || r.TotalSpotSavings >= 0.005 {
		fmt.Println("\nDISCOUNTS")
		fmt.Println("─────────")
		if r.TotalRISavings >= 0.005 {
			riCovered := 0
			for _, n := range r.Nodes {
				if n.RICost > 0 {
					riCovered++
				}
			}
			fmt.Printf("Reserved Instances: %d nodes, saving $%.2f/mo\n", riCovered, r.TotalRISavings)
		}
		if r.TotalSPSavings >= 0.005 {
			spCovered := 0
			for _, n := range r.Nodes {
				if n.SPCost > 0 {
					spCovered++
				}
			}
			fmt.Printf("Savings Plans:      %d nodes, saving $%.2f/mo\n", spCovered, r.TotalSPSavings)
		}
		if r.TotalSpotSavings >= 0.005 {
			fmt.Printf("Spot:               %d nodes, saving $%.2f/mo\n", r.SpotNodeCount, r.TotalSpotSavings)
		}
	}

	if len(r.Disks) > 0 || len(r.OrphanedDisks) > 0 {
		fmt.Println("\nSTORAGE")
		fmt.Println("───────")
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "DISK\tTYPE\tEST/MO\tACTUAL/MO\tDIFF")
		for _, d := range r.Disks {
			diff := fmtDiff(d.EstimatedCost, d.ActualCost)
			label := d.PVCNamespace + "/" + d.PVCName
			if d.MatchMethod == "os-disk" {
				label = d.PVCName // "(OS disk)"
			}
			fmt.Fprintf(w, "%s\t%s\t$%.2f\t$%.2f\t%s\n",
				ofmt.Truncate(d.DiskName, 25), ofmt.Truncate(label, 20),
				d.EstimatedCost, d.ActualCost, diff)
		}
		for _, d := range r.OrphanedDisks {
			fmt.Fprintf(w, "%s\t%s\t—\t$%.2f\t—\n",
				ofmt.Truncate(d.DiskName, 25), "(orphaned)", d.ActualCost)
		}
		w.Flush()
	}

	if len(r.LoadBalancers) > 0 {
		fmt.Println("\nLOAD BALANCERS")
		fmt.Println("──────────────")
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tSERVICE\tEST/MO\tACTUAL/MO\tDIFF")
		for _, lb := range r.LoadBalancers {
			diff := fmtDiff(lb.EstimatedCost, lb.ActualCost)
			svc := lb.ServiceNamespace + "/" + lb.ServiceName
			if lb.IsOrphaned {
				svc = "(orphaned)"
			}
			fmt.Fprintf(w, "%s\t%s\t$%.2f\t$%.2f\t%s\n",
				ofmt.Truncate(lb.LBName, 25), ofmt.Truncate(svc, 25),
				lb.EstimatedCost, lb.ActualCost, diff)
		}
		w.Flush()
	}

	if len(r.PublicIPs) > 0 {
		fmt.Println("\nPUBLIC IPs")
		fmt.Println("──────────")
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tACTUAL/MO")
		for _, ip := range r.PublicIPs {
			fmt.Fprintf(w, "%s\t$%.2f\n", ofmt.Truncate(ip.Name, 30), ip.ActualCost)
		}
		w.Flush()
	}

	if len(r.Namespaces) > 0 {
		fmt.Println("\nNAMESPACES")
		fmt.Println("──────────")
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAMESPACE\tEST/MO\tACTUAL/MO\tDIFF")
		for _, ns := range r.Namespaces {
			diff := fmtDiff(ns.EstimatedCost, ns.ActualCost)
			fmt.Fprintf(w, "%s\t$%.2f\t$%.2f\t%s\n",
				ofmt.Truncate(ns.Name, 25),
				ns.EstimatedCost,
				ns.ActualCost,
				diff)
		}
		w.Flush()
	}

	var alerts []string
	for _, n := range r.Nodes {
		if n.DriftAlert != "" {
			alerts = append(alerts, fmt.Sprintf("  %s: %s", ofmt.Truncate(n.NodeName, 25), n.DriftAlert))
		}
	}
	if len(alerts) > 0 {
		fmt.Println("\nALERTS")
		fmt.Println("──────")
		for _, a := range alerts {
			fmt.Println(a)
		}
	}

	if len(r.CoverageGaps) > 0 {
		fmt.Println("\nCOVERAGE GAPS")
		fmt.Println("─────────────")
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NODE\tTYPE\tCOST/MO\tPOTENTIAL SAVING")
		var totalSaving float64
		for _, g := range r.CoverageGaps {
			fmt.Fprintf(w, "%s\t%s\t$%.2f\t%s\n",
				ofmt.Truncate(g.NodeName, 25), g.InstanceType,
				g.MonthlyCost, g.Recommendation)
			totalSaving += g.PotentialSaving
		}
		w.Flush()
		fmt.Printf("  %d on-demand nodes could save $%.0f/mo with Reserved Instances\n",
			len(r.CoverageGaps), totalSaving)
	}

	fmt.Println("\nSUMMARY")
	fmt.Println("━━━━━━━")
	if r.InfraCost != nil {
		fmt.Printf("                  Estimated    Actual\n")
		fmt.Printf("Compute:          $%-12.2f $%.2f\n", r.InfraCost.ComputeEstimated, r.InfraCost.ComputeActual)
		if r.InfraCost.DiskActual > 0 || r.InfraCost.DiskEstimated > 0 {
			fmt.Printf("Storage:          $%-12.2f $%.2f\n", r.InfraCost.DiskEstimated, r.InfraCost.DiskActual)
		}
		if r.InfraCost.LBActual > 0 || r.InfraCost.LBEstimated > 0 {
			fmt.Printf("Load Balancers:   $%-12.2f $%.2f\n", r.InfraCost.LBEstimated, r.InfraCost.LBActual)
		}
		if r.InfraCost.PublicIPActual > 0 {
			fmt.Printf("Public IPs:       %-13s $%.2f\n", "—", r.InfraCost.PublicIPActual)
		}
		if r.InfraCost.UnmatchedCompute > 0.005 {
			fmt.Printf("Unreconciled:     %-13s $%.2f\n", "—", r.InfraCost.UnmatchedCompute)
		}
		if r.InfraCost.ManagementFee > 0 {
			fmt.Printf("Management Fee:   %-13s $%.2f\n", "—", r.InfraCost.ManagementFee)
		}
		fmt.Println("────────────────────────────────────")
		fmt.Printf("Total:            $%-12.2f $%.2f\n", r.InfraCost.TotalEstimated, r.InfraCost.TotalActual)
		totalDiff := r.InfraCost.TotalActual - r.InfraCost.TotalEstimated
		totalDiffPct := 0.0
		if r.InfraCost.TotalEstimated > 0 {
			totalDiffPct = totalDiff / r.InfraCost.TotalEstimated * 100
		}
		fmt.Printf("Difference:       $%+.2f (%+.1f%%)\n", totalDiff, totalDiffPct)

		// Show actual period spend (what Azure/AWS actually charged)
		days := r.PeriodEnd.Sub(r.PeriodStart).Hours() / 24
		if days > 0 {
			periodSpend := r.InfraCost.TotalActual / billing.DaysPerMonth * days
			fmt.Printf("\nPeriod spend:     $%.2f (%.0f days)\n", periodSpend, days)
		}
	} else {
		fmt.Printf("Estimated: $%.2f/mo\n", r.TotalEstimatedCost)
		fmt.Printf("Actual:    $%.2f/mo\n", r.TotalActualCost)
		fmt.Printf("Difference: $%+.2f (%+.1f%%)\n", r.TotalDifference, r.TotalDiffPercent)

		days := r.PeriodEnd.Sub(r.PeriodStart).Hours() / 24
		if days > 0 {
			periodSpend := r.TotalActualCost / billing.DaysPerMonth * days
			fmt.Printf("\nPeriod spend:     $%.2f (%.0f days)\n", periodSpend, days)
		}
	}

	if len(r.Warnings) > 0 {
		fmt.Println("\nWARNINGS")
		fmt.Println("────────")
		for _, w := range r.Warnings {
			fmt.Printf("  %s\n", w)
		}
	}

	if r.DaysFailed > 0 {
		fmt.Printf("\nWarning: %d/%d days queried (%d failed) — results may be incomplete\n",
			r.DaysQueried, r.DaysQueried+r.DaysFailed, r.DaysFailed)
	}
}

func printSetupGuide() {
	if reconcileProvider == "azure" {
		printAzureSetupGuide()
	} else {
		printAWSSetupGuide()
	}
}

func printAWSSetupGuide() {
	fmt.Println(`AWS CUR Reconciliation Setup Guide
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

Step 1: Enable CUR 2.0
  AWS Console → Billing → Data Exports → Create export
  - Type: Standard data export (CUR 2.0)
  - Include resource IDs: yes
  - Split cost allocation data: yes
  - Time granularity: Hourly
  - Format: Parquet
  - Athena integration: yes

Step 2: Wait for data (~24 hours)
  aws s3 ls s3://YOUR-CUR-BUCKET/cur/ --recursive

Step 3: Deploy Athena table (CloudFormation)
  aws s3 cp s3://YOUR-CUR-BUCKET/cur/YOUR-EXPORT/crawler-cfn.yml /tmp/
  aws cloudformation create-stack \
    --stack-name burn-cur-athena \
    --template-body file:///tmp/crawler-cfn.yml \
    --capabilities CAPABILITY_IAM

Step 4: Find your database and table
  aws glue get-databases --query 'DatabaseList[].Name'
  aws glue get-tables --database-name YOUR-DB --query 'TableList[].Name'

Step 5: Run reconciliation
  burn reconcile \
    --cur-database YOUR-DB \
    --cur-table YOUR-TABLE \
    --cur-output s3://YOUR-CUR-BUCKET/athena-results/ \
    --cur-region YOUR-REGION \
    --days 7

IAM permissions:
  athena:StartQueryExecution, athena:GetQueryExecution,
  athena:GetQueryResults, athena:StopQueryExecution,
  s3:GetObject, s3:PutObject, s3:ListBucket,
  glue:GetTable, glue:GetPartitions, glue:GetDatabase`)
}

func printAzureSetupGuide() {
	fmt.Println(`Azure Cost Reconciliation Setup Guide
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

Prerequisites:
  - Pay-As-You-Go, EA, or MCA subscription (not Free Trial)
  - Cost Management API access

Step 1: Find your subscription ID
  az account show --query id -o tsv

Step 2: Authenticate
  Option A — Interactive (local development):
    az login

  Option B — Service Principal (CI/CD):
    az ad sp create-for-rbac --name burn-reader \
      --role "Cost Management Reader" \
      --scopes /subscriptions/YOUR-SUBSCRIPTION-ID

    export AZURE_TENANT_ID=...
    export AZURE_CLIENT_ID=...
    export AZURE_CLIENT_SECRET=...

  Option C — Managed Identity (AKS):
    No extra config needed if the pod has Cost Management Reader role.

Step 3: Run reconciliation
  burn reconcile --provider azure \
    --azure-subscription YOUR-SUBSCRIPTION-ID \
    --days 7

  Or with environment variable:
    export AZURE_SUBSCRIPTION_ID=YOUR-SUBSCRIPTION-ID
    burn reconcile --provider azure --days 7

Options:
  --cost-type amortized   Spread RI/SP costs across term (default)
  --cost-type actual      Show actual charges as invoiced
  --ai                    Get AI analysis of results

Required permissions:
  Microsoft.CostManagement/query/action

Data availability:
  EA/MCA: 8-24 hours after usage
  PAYG: up to 72 hours after usage`)
}
