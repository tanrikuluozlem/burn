package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/tanrikuluozlem/burn/internal/advisor"
	"github.com/tanrikuluozlem/burn/internal/analyzer"
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
	f.StringVar(&prometheusURL, "prometheus", "", "Prometheus server URL")
	f.BoolVarP(&verbose, "verbose", "v", false, "verbose output")
	f.BoolVar(&askSlack, "slack", false, "send answer to Slack")
	f.StringVar(&askSlackWebhook, "slack-webhook", "", "Slack webhook URL (or set SLACK_WEBHOOK_URL)")

	rootCmd.AddCommand(askCmd)
}

func runAsk(cmd *cobra.Command, args []string) error {
	question := strings.Join(args, " ")

	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return fmt.Errorf("ANTHROPIC_API_KEY not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	fmt.Println("Collecting cluster data...")

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

	fmt.Println("Thinking...")

	answer, err := advisor.New(apiKey).Ask(ctx, report, question)
	if err != nil {
		return err
	}

	fmt.Println(answer)

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
		fmt.Println("\n✓ Answer sent to Slack")
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
