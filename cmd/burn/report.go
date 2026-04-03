package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/ozlemtanrikulu/burn/internal/advisor"
	"github.com/ozlemtanrikulu/burn/internal/analyzer"
	"github.com/ozlemtanrikulu/burn/internal/collector"
	"github.com/ozlemtanrikulu/burn/internal/pricing"
	"github.com/ozlemtanrikulu/burn/internal/slack"
	"github.com/spf13/cobra"
)

var reportCmd = &cobra.Command{
	Use:   "report",
	Short: "Send cost report to Slack",
	RunE:  runReport,
}

func init() {
	reportCmd.Flags().StringP("slack-webhook", "s", "", "Slack webhook URL")
	reportCmd.Flags().Bool("ai", false, "include AI recommendations")
	rootCmd.AddCommand(reportCmd)
}

func runReport(cmd *cobra.Command, _ []string) error {
	webhook, _ := cmd.Flags().GetString("slack-webhook")
	if webhook == "" {
		webhook = os.Getenv("SLACK_WEBHOOK_URL")
	}
	if webhook == "" {
		return fmt.Errorf("missing slack webhook: use --slack-webhook or set SLACK_WEBHOOK_URL")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	coll, err := collector.New(kubeconfig, os.Getenv("PROMETHEUS_URL"))
	if err != nil {
		return err
	}

	cluster, err := coll.Collect(ctx)
	if err != nil {
		return err
	}

	pp, err := pricing.NewCloudPricingProvider(ctx)
	if err != nil {
		return err
	}

	report, err := analyzer.New(pp).Analyze(ctx, cluster)
	if err != nil {
		return err
	}

	sc := slack.NewWebhookClient(webhook)
	if err := sc.Send(ctx, slack.FormatCostReport(report)); err != nil {
		return err
	}
	fmt.Println("report sent")

	withAI, _ := cmd.Flags().GetBool("ai")
	if !withAI {
		return nil
	}

	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return fmt.Errorf("ANTHROPIC_API_KEY not set")
	}

	aiReport, err := advisor.New(apiKey).Analyze(ctx, report)
	if err != nil {
		return err
	}

	if err := sc.Send(ctx, slack.FormatAIReport(aiReport)); err != nil {
		return err
	}
	fmt.Println("ai recommendations sent")

	return nil
}
