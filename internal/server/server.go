package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/tanrikuluozlem/burn/internal/advisor"
	"github.com/tanrikuluozlem/burn/internal/analyzer"
	"github.com/tanrikuluozlem/burn/internal/collector"
	"github.com/tanrikuluozlem/burn/internal/pricing"
)

type Config struct {
	Port          int
	Kubeconfig    string
	PrometheusURL string
	APIKey        string
	SigningSecret string
}

type Server struct {
	config     Config
	httpServer *http.Server
	collector  *collector.Collector
	advisor    *advisor.Advisor
}

func New(cfg Config) (*Server, error) {
	coll, err := collector.New(cfg.Kubeconfig, cfg.PrometheusURL)
	if err != nil {
		return nil, fmt.Errorf("failed to create collector: %w", err)
	}

	s := &Server{
		config:    cfg,
		collector: coll,
		advisor:   advisor.New(cfg.APIKey),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/slack", s.handleSlack)

	s.httpServer = &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Port),
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 60 * time.Second,
	}

	return s, nil
}

func (s *Server) Start() error {
	return s.httpServer.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

func (s *Server) handleSlack(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Verify Slack signature
	if err := verifySlackSignature(r, s.config.SigningSecret); err != nil {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	text := strings.TrimSpace(r.FormValue("text"))
	responseURL := r.FormValue("response_url")

	// Immediate acknowledgment
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"response_type": "ephemeral",
		"text":          "Analyzing cluster...",
	})

	// Process in background
	go s.processSlackCommand(text, responseURL)
}

func (s *Server) processSlackCommand(text, responseURL string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	var response string
	var err error

	// Parse command
	text = strings.TrimSpace(text)

	switch {
	case strings.HasPrefix(text, "ask "):
		question := strings.TrimPrefix(text, "ask ")
		question = strings.Trim(question, "\"'")
		response, err = s.handleAsk(ctx, question)
	case text == "" || text == "analyze":
		response, err = s.handleAnalyze(ctx)
	default:
		response = fmt.Sprintf("Unknown command: %s\n\nUsage:\n  /burn analyze\n  /burn ask \"your question\"", text)
	}

	if err != nil {
		response = fmt.Sprintf("Error: %v", err)
	}

	// Send response to Slack
	s.sendSlackResponse(responseURL, response)
}

func (s *Server) handleAsk(ctx context.Context, question string) (string, error) {
	info, err := s.collector.Collect(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to collect cluster data: %w", err)
	}

	pp, err := pricing.NewCloudPricingProvider(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get pricing: %w", err)
	}

	report, err := analyzer.New(pp).Analyze(ctx, info)
	if err != nil {
		return "", fmt.Errorf("failed to analyze: %w", err)
	}

	return s.advisor.Ask(ctx, report, question)
}

func (s *Server) handleAnalyze(ctx context.Context) (string, error) {
	info, err := s.collector.Collect(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to collect cluster data: %w", err)
	}

	pp, err := pricing.NewCloudPricingProvider(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get pricing: %w", err)
	}

	report, err := analyzer.New(pp).Analyze(ctx, info)
	if err != nil {
		return "", fmt.Errorf("failed to analyze: %w", err)
	}

	// Format summary
	return fmt.Sprintf("*Cluster Cost Summary*\n"+
		"Nodes: %d | Pods: %d\n"+
		"Monthly: $%.2f\n"+
		"Potential Savings: $%.2f/mo",
		report.TotalNodes, report.TotalPods,
		report.MonthlyCost, report.WasteAnalysis.PotentialSavings), nil
}

func (s *Server) sendSlackResponse(responseURL, text string) {
	if responseURL == "" {
		return
	}

	payload := map[string]any{
		"response_type": "in_channel",
		"blocks": []map[string]any{
			{
				"type": "section",
				"text": map[string]string{
					"type": "mrkdwn",
					"text": text,
				},
			},
		},
	}

	body, _ := json.Marshal(payload)
	http.Post(responseURL, "application/json", strings.NewReader(string(body)))
}
