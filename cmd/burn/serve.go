package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/tanrikuluozlem/burn/internal/server"
	"github.com/spf13/cobra"
)

var (
	servePort        int
	serveKubeconfig  string
	serveKubecontext string
	servePrometheus  string
	servePeriod      string
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start HTTP server for Slack slash commands",
	Long: `Start an HTTP server that responds to Slack slash commands.

Endpoints:
  POST /slack    - Slack slash command handler
  GET  /health   - Health check

Environment variables:
  ANTHROPIC_API_KEY     - Required for AI features
  SLACK_SIGNING_SECRET  - Required for request verification

Example:
  burn serve --port 8080`,
	RunE: runServe,
}

func init() {
	f := serveCmd.Flags()
	f.IntVarP(&servePort, "port", "p", 8080, "port to listen on")
	f.StringVar(&serveKubeconfig, "kubeconfig", "", "path to kubeconfig file")
	f.StringVar(&serveKubecontext, "context", "", "kubeconfig context to use")
	f.StringVar(&servePrometheus, "prometheus", "", "Prometheus server URL")
	f.StringVar(&servePeriod, "period", "", "analysis period (e.g. 1h, 7d, 30d)")

	rootCmd.AddCommand(serveCmd)
}

func runServe(cmd *cobra.Command, args []string) error {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return fmt.Errorf("ANTHROPIC_API_KEY not set")
	}

	signingSecret := os.Getenv("SLACK_SIGNING_SECRET")
	if signingSecret == "" {
		return fmt.Errorf("SLACK_SIGNING_SECRET not set")
	}

	srv, err := server.New(server.Config{
		Port:          servePort,
		Kubeconfig:    serveKubeconfig,
		Kubecontext:   serveKubecontext,
		PrometheusURL: servePrometheus,
		Period:        servePeriod,
		APIKey:        apiKey,
		SigningSecret: signingSecret,
	})
	if err != nil {
		return err
	}

	// Graceful shutdown
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	go func() {
		log.Printf("Starting server on :%d", servePort)
		if err := srv.Start(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server error: %v", err)
		}
	}()

	<-stop
	log.Println("Shutting down...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	return srv.Shutdown(ctx)
}
