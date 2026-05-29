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
	"github.com/tanrikuluozlem/burn/internal/billing"
	"github.com/tanrikuluozlem/burn/internal/collector"
	ofmt "github.com/tanrikuluozlem/burn/internal/output"
	"github.com/tanrikuluozlem/burn/internal/pricing"
	"github.com/spf13/cobra"
)

var (
	curDatabase    string
	curTable       string
	curOutputLoc   string
	curWorkgroup   string
	curRegion      string
	curDays        int
	reconcileOut   string
	reconcileAI    bool
	reconcileSetup bool
)

var reconcileCmd = &cobra.Command{
	Use:   "reconcile",
	Short: "Compare estimated costs with actual AWS bill",
	RunE:  runReconcile,
}

func init() {
	f := reconcileCmd.Flags()
	f.StringVar(&curDatabase, "cur-database", "", "Athena database for CUR (or CUR_DATABASE env)")
	f.StringVar(&curTable, "cur-table", "", "Athena table for CUR (or CUR_TABLE env)")
	f.StringVar(&curOutputLoc, "cur-output", "", "S3 output for Athena results (or CUR_OUTPUT_LOCATION env)")
	f.StringVar(&curWorkgroup, "cur-workgroup", "primary", "Athena workgroup (or CUR_WORKGROUP env)")
	f.StringVar(&curRegion, "cur-region", "", "AWS region for Athena (or CUR_REGION env)")
	f.IntVar(&curDays, "days", 7, "number of days to reconcile")
	f.StringVar(&kubeconfig, "kubeconfig", "", "path to kubeconfig file")
	f.StringVar(&kubecontext, "context", "", "kubeconfig context to use")
	f.StringVarP(&reconcileOut, "output", "o", "table", "output format (table|json)")
	f.BoolVar(&reconcileAI, "ai", false, "get AI analysis of reconciliation results")
	f.BoolVar(&reconcileSetup, "setup", false, "show CUR + Athena setup instructions")
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
	if v := os.Getenv("CUR_REGION"); v != "" && curRegion == "" {
		curRegion = v
	}

	if curDays < 1 {
		return fmt.Errorf("--days must be at least 1")
	}

	timeout := time.Duration(curDays+5) * time.Minute
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

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

	prometheusURL := os.Getenv("PROMETHEUS_URL")
	coll, err := collector.New(kubeconfig, kubecontext, "", prometheusURL, "")
	if err != nil {
		return err
	}

	pp, err := pricing.NewCloudPricingProvider(ctx)
	if err != nil {
		return err
	}

	fmt.Println("Collecting cluster data...")
	info, err := coll.Collect(ctx)
	if err != nil {
		return err
	}

	report, err := analyzer.New(pp).Analyze(ctx, info)
	if err != nil {
		return err
	}

	end := time.Now().UTC().Add(-48 * time.Hour)
	start := end.AddDate(0, 0, -curDays)

	fmt.Printf("Querying CUR via Athena (%s to %s)...\n",
		start.Format("Jan 2"), end.Format("Jan 2, 2006"))

	reconciler := billing.NewReconciler(athenaClient)
	result, err := reconciler.Reconcile(ctx, report, info, start, end)
	if err != nil {
		return err
	}

	switch reconcileOut {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	default:
		outputReconcileTable(result)
	}

	if reconcileAI {
		apiKey := os.Getenv("ANTHROPIC_API_KEY")
		if apiKey == "" {
			return fmt.Errorf("ANTHROPIC_API_KEY not set")
		}

		resultJSON, _ := json.MarshalIndent(result, "", "  ")
		question := fmt.Sprintf("Analyze this CUR reconciliation report. Explain why the estimated vs actual costs differ. What discounts are applied? What actions should be taken?\n\n%s", string(resultJSON))

		fmt.Println("\nfetching AI analysis...")
		answer, err := advisor.New(apiKey).AskStream(ctx, report, question, func(text string) {
			fmt.Print(text)
		})
		if err != nil {
			return err
		}
		_ = answer
		fmt.Println()
	}

	return nil
}

func outputReconcileTable(r *billing.ReconciliationReport) {
	fmt.Println()
	fmt.Printf("CUR Reconciliation (%s - %s)\n",
		r.PeriodStart.Format("Jan 2"), r.PeriodEnd.Format("Jan 2, 2006"))
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
		diff := ""
		if n.ActualCost > 0 {
			diff = fmt.Sprintf("%+.0f%%", n.DifferencePercent)
		}
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

	if r.RINodeCount > 0 || r.SPNodeCount > 0 || r.SpotNodeCount > 0 {
		fmt.Println("\nDISCOUNTS")
		fmt.Println("─────────")
		if r.RINodeCount > 0 {
			fmt.Printf("Reserved Instances: %d nodes, saving $%.2f/mo\n", r.RINodeCount, r.TotalRISavings)
		}
		if r.SPNodeCount > 0 {
			fmt.Printf("Savings Plans:      %d nodes, saving $%.2f/mo\n", r.SPNodeCount, r.TotalSPSavings)
		}
		if r.SpotNodeCount > 0 {
			fmt.Printf("Spot:               %d nodes, saving $%.2f/mo\n", r.SpotNodeCount, r.TotalSpotSavings)
		}
	}

	if len(r.Namespaces) > 0 {
		fmt.Println("\nNAMESPACES")
		fmt.Println("──────────")
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAMESPACE\tEST/MO\tACTUAL/MO\tDIFF")
		for _, ns := range r.Namespaces {
			diff := ""
			if ns.ActualCost > 0 {
				diff = fmt.Sprintf("%+.0f%%", ns.DiffPercent)
			}
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

	fmt.Println("\nSUMMARY")
	fmt.Println("━━━━━━━")
	fmt.Printf("Estimated: $%.2f/mo\n", r.TotalEstimatedCost)
	fmt.Printf("Actual:    $%.2f/mo\n", r.TotalActualCost)
	fmt.Printf("Difference: $%+.2f (%+.1f%%)\n", r.TotalDifference, r.TotalDiffPercent)

	if r.UnmatchedNodes > 0 || r.UnmatchedCURItems > 0 || r.DaysFailed > 0 {
		fmt.Print("\nDiagnostics:")
		if r.DaysFailed > 0 {
			fmt.Printf(" %d/%d days queried (%d failed)", r.DaysQueried, r.DaysQueried+r.DaysFailed, r.DaysFailed)
		}
		if r.UnmatchedNodes > 0 || r.UnmatchedCURItems > 0 {
			fmt.Printf(" %d unmatched nodes, %d unmatched CUR items", r.UnmatchedNodes, r.UnmatchedCURItems)
		}
		fmt.Println()
	}

	if r.DataScannedBytes > 0 {
		mb := float64(r.DataScannedBytes) / (1024 * 1024)
		cost := float64(r.DataScannedBytes) / (1024 * 1024 * 1024 * 1024) * 5.0
		fmt.Printf("Athena: %.1f MB scanned (~$%.4f)\n", mb, cost)
	}
}

func printSetupGuide() {
	fmt.Println(`CUR Reconciliation Setup Guide
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

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

IAM Policy (minimum permissions):
  athena:StartQueryExecution, athena:GetQueryExecution,
  athena:GetQueryResults, athena:StopQueryExecution,
  s3:GetObject, s3:PutObject, s3:ListBucket,
  glue:GetTable, glue:GetPartitions, glue:GetDatabase`)
}
