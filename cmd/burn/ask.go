package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/tanrikuluozlem/burn/internal/advisor"
	"github.com/tanrikuluozlem/burn/internal/analyzer"
	"github.com/tanrikuluozlem/burn/internal/billing"
	"github.com/tanrikuluozlem/burn/internal/collector"
	"github.com/tanrikuluozlem/burn/internal/pricing"
	"github.com/tanrikuluozlem/burn/internal/slack"
	"github.com/spf13/cobra"
)

var (
	askSlack        bool
	askSlackWebhook string
)

var askCmd = &cobra.Command{
	Use:   "ask [question]",
	Short: "Ask questions about your cluster costs in natural language",
	Long: `Ask questions about your Kubernetes cluster costs using natural language.

Examples:
  burn ask "why is this node so expensive?"
  burn ask "which nodes should I convert to spot?"
  burn ask "how can I reduce costs?"
  burn ask "what's the risk of using spot instances?"
  burn ask "should I remove any nodes?"`,
	Args: cobra.MinimumNArgs(1),
	RunE: runAsk,
}

func init() {
	f := askCmd.Flags()
	f.StringVarP(&namespace, "namespace", "n", "", "target namespace (default: all)")
	f.StringVar(&kubeconfig, "kubeconfig", "", "path to kubeconfig file")
	f.StringVar(&kubecontext, "context", "", "kubeconfig context to use")
	f.StringVar(&prometheusURL, "prometheus", "", "Prometheus server URL")
	f.StringVar(&period, "period", "", "analysis period (e.g. 1h, 7d, 30d)")
	f.BoolVarP(&verbose, "verbose", "v", false, "verbose output")
	f.BoolVar(&askSlack, "slack", false, "send answer to Slack")
	f.StringVar(&askSlackWebhook, "slack-webhook", "", "Slack webhook URL (or set SLACK_WEBHOOK_URL)")

	rootCmd.AddCommand(askCmd)
}

func runAsk(cmd *cobra.Command, args []string) error {
	question := strings.Join(args, " ")

	if verbose {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
			Level: slog.LevelDebug,
		})))
	}

	if period != "" && !isValidPeriod(period) {
		return fmt.Errorf("invalid period %q: use Prometheus duration format (e.g. 1h, 7d, 30d)", period)
	}

	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return fmt.Errorf("ANTHROPIC_API_KEY not set")
	}

	if p := os.Getenv("PROMETHEUS_URL"); p != "" && prometheusURL == "" {
		prometheusURL = p
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	fmt.Fprintln(os.Stderr, "Collecting cluster data...")

	coll, err := collector.New(kubeconfig, kubecontext, namespace, prometheusURL, period)
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

	// Detect cloud from node labels, not env vars
	cloud := collector.CloudUnknown
	for _, n := range info.Nodes {
		if n.CloudProvider != collector.CloudUnknown {
			cloud = n.CloudProvider
			break
		}
	}

	var billingContext string
	if cloud == collector.CloudAzure {
		if sub := os.Getenv("AZURE_SUBSCRIPTION_ID"); sub != "" {
			azureCfg := billing.AzureConfig{SubscriptionID: sub, CostType: "amortized"}
			azureClient, err := billing.NewAzureCostClient(ctx, azureCfg)
			if err == nil {
				dataDelay := 48 * time.Hour
				end := time.Now().UTC().Add(-dataDelay)
				start := end.AddDate(0, 0, -7)
				estimatedCosts, pvEstimates, lbEstimates := billing.BuildEstimateMaps(report)
				result, err := billing.ReconcileAzure(ctx, azureClient, info.Nodes, estimatedCosts, report.Namespaces, info.PVCs, pvEstimates, info.LoadBalancers, lbEstimates, start, end, 7)
				if err == nil {
					billing.EnrichCoverageGaps(ctx, result.CoverageGaps, pp)
					if data, err := json.Marshal(result); err == nil {
						billingContext = string(data)
					}
				}
			}
		}
	} else if curDB := os.Getenv("CUR_DATABASE"); curDB != "" {
		cfg := billing.AthenaConfig{
			Database:       curDB,
			Table:          os.Getenv("CUR_TABLE"),
			OutputLocation: os.Getenv("CUR_OUTPUT_LOCATION"),
			WorkGroup:      os.Getenv("CUR_WORKGROUP"),
			Region:         os.Getenv("CUR_REGION"),
		}
		if cfg.WorkGroup == "" {
			cfg.WorkGroup = "primary"
		}
		if billing.ValidateAthenaConfig(cfg) == nil {
			athenaClient, err := billing.NewAthenaClient(ctx, cfg)
			if err == nil {
				dataDelay := 48 * time.Hour
				end := time.Now().UTC().Add(-dataDelay)
				start := end.AddDate(0, 0, -7)
				reconciler := billing.NewReconciler(athenaClient)
				result, err := reconciler.Reconcile(ctx, report, info, start, end)
				if err == nil {
					billing.EnrichCoverageGaps(ctx, result.CoverageGaps, pp)
					if data, err := json.Marshal(result); err == nil {
						billingContext = string(data)
					}
				}
			}
		}
	}

	fmt.Fprintln(os.Stderr, "Thinking...")

	answer, err := advisor.New(apiKey).AskStream(ctx, report, question, func(text string) {
		fmt.Print(text)
	}, billingContext)
	if err != nil {
		return err
	}
	fmt.Println()
	fmt.Fprintln(os.Stderr, "\nAnalysis based on cluster data. Run burn reconcile to verify.")

	if askSlack {
		webhook := askSlackWebhook
		if webhook == "" {
			webhook = os.Getenv("SLACK_WEBHOOK_URL")
		}
		if webhook == "" {
			return fmt.Errorf("--slack requires --slack-webhook or SLACK_WEBHOOK_URL env var")
		}

		sc := slack.NewWebhookClient(webhook)
		msg := formatAskSlackMessage(question, answer)
		if err := sc.Send(ctx, msg); err != nil {
			return fmt.Errorf("failed to send to Slack: %w", err)
		}
		fmt.Fprintln(os.Stderr, "\n✓ Answer sent to Slack")
	}

	return nil
}

func formatAskSlackMessage(question, answer string) *slack.Message {
	return &slack.Message{
		Blocks: []slack.Block{
			{
				Type: "header",
				Text: &slack.TextObject{
					Type: "plain_text",
					Text: "Burn AI Assistant",
				},
			},
			{
				Type: "section",
				Text: &slack.TextObject{
					Type: "mrkdwn",
					Text: fmt.Sprintf("*Question:* %s", question),
				},
			},
			{
				Type: "divider",
			},
			{
				Type: "section",
				Text: &slack.TextObject{
					Type: "mrkdwn",
					Text: answer,
				},
			},
		},
	}
}
