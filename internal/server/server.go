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
	Kubecontext   string
	Namespace     string
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
	coll, err := collector.New(cfg.Kubeconfig, cfg.Kubecontext, cfg.Namespace, cfg.PrometheusURL)
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
	case strings.HasPrefix(text, "namespace "), strings.HasPrefix(text, "ns "):
		ns := text
		ns = strings.TrimPrefix(ns, "namespace ")
		ns = strings.TrimPrefix(ns, "ns ")
		ns = strings.TrimSpace(ns)
		response, err = s.handleNamespace(ctx, ns)
	case text == "" || text == "analyze":
		response, err = s.handleAnalyze(ctx)
	default:
		response = fmt.Sprintf("Unknown command: %s\n\nUsage:\n  /burn — namespace cost summary\n  /burn ns <name> — pod details\n  /burn ask \"your question\"", text)
	}

	if err != nil {
		response = fmt.Sprintf("Error: %v", err)
	}

	// Send response to Slack
	s.sendSlackResponse(responseURL, response)
}

func (s *Server) handleAsk(ctx context.Context, question string) (string, error) {
	report, err := s.getReport(ctx)
	if err != nil {
		return "", err
	}
	return s.advisor.Ask(ctx, report, question)
}

func (s *Server) handleAnalyze(ctx context.Context) (string, error) {
	report, err := s.getReport(ctx)
	if err != nil {
		return "", err
	}

	idlePercent := 0.0
	if report.MonthlyCost > 0 {
		idlePercent = (report.TotalIdleCost / report.MonthlyCost) * 100
	}

	summary := fmt.Sprintf("*Kubernetes Cost Report*\n"+
		"💰 Monthly: $%.0f | Idle: $%.0f (%.0f%%)\n"+
		"📦 Nodes: %d | Pods: %d",
		report.MonthlyCost, report.TotalIdleCost, idlePercent,
		report.TotalNodes, report.TotalPods)

	hasPrometheus := report.MetricsSource == "prometheus"
	if len(report.Namespaces) > 0 {
		summary += "\n\n*Cost by Namespace:*"
		var allocated float64
		for _, ns := range report.Namespaces {
			allocated += ns.MonthlyCost
			if hasPrometheus {
				summary += fmt.Sprintf("\n• `%s` — %d pods — $%.0f/mo\n    CPU: %s req → %s used | MEM: %s req → %s used",
					ns.Name, ns.PodCount, ns.MonthlyCost,
					formatMillicores(ns.CPURequest), formatCores(ns.CPUUsage),
					formatBytes(ns.MemRequest), formatBytes(ns.MemUsage))
			} else {
				summary += fmt.Sprintf("\n• `%s` — %d pods — $%.0f/mo", ns.Name, ns.PodCount, ns.MonthlyCost)
			}
		}
		idle := report.MonthlyCost - allocated
		if idle > 0 {
			summary += fmt.Sprintf("\n• _Idle (unallocated)_ — $%.0f/mo", idle)
		}
		summary += fmt.Sprintf("\n*Total: $%.0f/mo*", report.MonthlyCost)
	}

	if report.WasteAnalysis.PotentialSavings > 0 {
		summary += fmt.Sprintf("\n\n_Potential savings: $%.0f/mo_", report.WasteAnalysis.PotentialSavings)
	}

	return summary, nil
}

func (s *Server) handleNamespace(ctx context.Context, ns string) (string, error) {
	report, err := s.getReport(ctx)
	if err != nil {
		return "", err
	}

	var pods []analyzer.PodEfficiency
	for _, p := range report.AllPods {
		if p.Namespace == ns {
			pods = append(pods, p)
		}
	}

	if len(pods) == 0 {
		return fmt.Sprintf("No pods found in namespace `%s`", ns), nil
	}

	var totalCost float64
	for _, p := range pods {
		totalCost += p.MonthlyCost
	}

	hasPrometheus := report.MetricsSource == "prometheus"
	result := fmt.Sprintf("*Namespace: %s* (%d pods, $%.0f/mo)\n", ns, len(pods), totalCost)

	for _, p := range pods {
		if hasPrometheus {
			result += fmt.Sprintf("\n• `%s` — $%.0f/mo\n    CPU: %s req → %s used | MEM: %s req → %s used",
				p.Name, p.MonthlyCost,
				formatMillicores(p.CPURequest), formatCores(p.CPUUsage),
				formatBytes(p.MemRequest), formatBytes(p.MemUsage))
		} else {
			result += fmt.Sprintf("\n• `%s` — $%.0f/mo", p.Name, p.MonthlyCost)
		}
	}

	return result, nil
}

func (s *Server) getReport(ctx context.Context) (*analyzer.CostReport, error) {
	info, err := s.collector.Collect(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to collect cluster data: %w", err)
	}

	pp, err := pricing.NewCloudPricingProvider(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get pricing: %w", err)
	}

	return analyzer.New(pp).Analyze(ctx, info)
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
	resp, err := http.Post(responseURL, "application/json", strings.NewReader(string(body)))
	if err != nil {
		fmt.Printf("failed to send slack response: %v\n", err)
		return
	}
	resp.Body.Close()
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
