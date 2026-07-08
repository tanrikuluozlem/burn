package main

import (
	"context"
	"os"

	"github.com/spf13/cobra"
	"github.com/tanrikuluozlem/burn/internal/mcpserver"
	"github.com/tanrikuluozlem/burn/internal/pricing"
)

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Start MCP server for AI agent integration",
	Long: `Start a Model Context Protocol server over stdin/stdout.

Works with Claude Code, Cursor, and any MCP-compatible AI agent.

  claude mcp add burn -- burn mcp --prometheus http://prometheus:9090`,
	RunE: runMCP,
}

func init() {
	f := mcpCmd.Flags()
	f.StringVar(&kubeconfig, "kubeconfig", "", "path to kubeconfig file")
	f.StringVar(&kubecontext, "context", "", "kubeconfig context to use")
	f.StringVar(&prometheusURL, "prometheus", "", "Prometheus server URL")
	f.StringVar(&period, "period", "", "default analysis period (e.g. 1h, 7d, 30d)")

	rootCmd.AddCommand(mcpCmd)
}

func runMCP(_ *cobra.Command, _ []string) error {
	if p := os.Getenv("PROMETHEUS_URL"); p != "" && prometheusURL == "" {
		prometheusURL = p
	}

	ctx := context.Background()

	pp, err := pricing.NewCloudPricingProvider(ctx)
	if err != nil {
		return err
	}

	cfg := mcpserver.Config{
		Kubeconfig:    kubeconfig,
		Kubecontext:   kubecontext,
		PrometheusURL: prometheusURL,
		Period:        period,
	}

	srv := mcpserver.New(cfg, pp, version)
	return srv.Run(ctx)
}
