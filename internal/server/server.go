package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/tanrikuluozlem/burn/internal/advisor"
	"github.com/tanrikuluozlem/burn/internal/analyzer"
	"github.com/tanrikuluozlem/burn/internal/billing"
	"github.com/tanrikuluozlem/burn/internal/collector"
	"github.com/tanrikuluozlem/burn/internal/output"
	"github.com/tanrikuluozlem/burn/internal/pricing"
)

type Config struct {
	Port          int
	Kubeconfig    string
	Kubecontext   string
	Namespace     string
	PrometheusURL string
	Period        string
	APIKey        string
	SigningSecret string
}

type Server struct {
	config      Config
	httpServer  *http.Server
	collector   *collector.Collector
	advisor     *advisor.Advisor
	wg          sync.WaitGroup
	activeReqs  int
	reqMu       sync.Mutex
	maxActiveReqs int
}

func New(cfg Config) (*Server, error) {
	coll, err := collector.New(cfg.Kubeconfig, cfg.Kubecontext, cfg.Namespace, cfg.PrometheusURL, cfg.Period)
	if err != nil {
		return nil, fmt.Errorf("failed to create collector: %w", err)
	}

	s := &Server{
		config:        cfg,
		collector:     coll,
		advisor:       advisor.New(cfg.APIKey),
		maxActiveReqs: 5,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/slack", s.handleSlack)

	s.httpServer = &http.Server{
		Addr:           fmt.Sprintf(":%d", cfg.Port),
		Handler:        mux,
		ReadTimeout:    10 * time.Second,
		WriteTimeout:   60 * time.Second,
		IdleTimeout:    120 * time.Second,
		MaxHeaderBytes: 1 << 20, // 1MB
	}

	return s, nil
}

func (s *Server) Start() error {
	return s.httpServer.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
	// Shutdown HTTP server (stops accepting new requests)
	if err := s.httpServer.Shutdown(ctx); err != nil {
		return err
	}
	// Wait for background goroutines to finish
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
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

	// Limit request body size (1MB)
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

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
	if len(text) > 4096 {
		http.Error(w, "command too long", http.StatusBadRequest)
		return
	}
	responseURL := r.FormValue("response_url")

	// Validate response URL is from Slack
	if responseURL != "" {
		parsedURL, parseErr := url.Parse(responseURL)
		if parseErr != nil || parsedURL.Scheme != "https" || parsedURL.Host != "hooks.slack.com" {
			http.Error(w, "invalid response URL", http.StatusBadRequest)
			return
		}
	}

	// Rate limit: reject if too many active requests
	s.reqMu.Lock()
	if s.activeReqs >= s.maxActiveReqs {
		s.reqMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"response_type": "ephemeral",
			"text":          "Rate limit exceeded. Try again shortly.",
		})
		return
	}
	s.activeReqs++
	s.reqMu.Unlock()

	// Immediate acknowledgment
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"response_type": "ephemeral",
		"text":          "Analyzing cluster...",
	})

	// Process in background with goroutine tracking
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			s.reqMu.Lock()
			s.activeReqs--
			s.reqMu.Unlock()
		}()
		s.processSlackCommand(text, responseURL)
	}()
}

func (s *Server) processSlackCommand(text, responseURL string) {
	timeout := 2 * time.Minute
	if text == "reconcile" {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
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
	case text == "reconcile" || strings.HasPrefix(text, "reconcile "):
		response, err = s.handleReconcile(ctx, text)
	case text == "" || text == "analyze":
		response, err = s.handleAnalyze(ctx)
	default:
		response = fmt.Sprintf("Unknown command: %s\n\nUsage:\n  /burn — cost summary\n  /burn ns <name> — pod details\n  /burn ask \"question\" — AI analysis\n  /burn reconcile — AWS CUR reconciliation\n  /burn reconcile --provider azure --azure-subscription <id>\n  /burn reconcile --days 7", text)
	}

	if err != nil {
		fmt.Printf("slack command error: %v\n", err)
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
	answer, err := s.advisor.Ask(ctx, report, question)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("*Q: %s*\n\n%s", question, answer), nil
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

	header := "*Kubernetes Cost Report*"
	if report.Period != "" {
		header = fmt.Sprintf("*Kubernetes Cost Report (%s avg)*", report.Period)
	}
	summary := fmt.Sprintf("%s\n"+
		"💰 Monthly: $%.2f | Idle: $%.2f (%.0f%%)\n"+
		"📦 Nodes: %d | Pods: %d",
		header,
		report.MonthlyCost, report.TotalIdleCost, idlePercent,
		report.TotalNodes, report.TotalPods)

	hasPrometheus := report.MetricsSource == "prometheus"
	if len(report.Namespaces) > 0 {
		summary += "\n\n*Cost by Namespace:*"
		var allocated float64
		for _, ns := range report.Namespaces {
			allocated += ns.MonthlyCost
			if hasPrometheus {
				summary += fmt.Sprintf("\n• `%s` — %d pods — $%.2f/mo\n    CPU: %s req → %s used | MEM: %s req → %s used",
					ns.Name, ns.PodCount, ns.MonthlyCost,
					output.FormatMillicores(ns.CPURequest), output.FormatCores(ns.CPUUsage),
					output.FormatBytes(ns.MemRequest), output.FormatBytes(ns.MemUsage))
			} else {
				summary += fmt.Sprintf("\n• `%s` — %d pods — $%.2f/mo", ns.Name, ns.PodCount, ns.MonthlyCost)
			}
		}
		idle := report.MonthlyCost - allocated
		if idle > 0 {
			summary += fmt.Sprintf("\n• _Idle (unallocated)_ — $%.2f/mo", idle)
		}
		summary += fmt.Sprintf("\n*Total: $%.2f/mo*", report.MonthlyCost)
	}

	// Storage
	if len(report.PVCosts) > 0 {
		summary += "\n\n*Storage:*"
		for _, pv := range report.PVCosts {
			summary += fmt.Sprintf("\n• `%s` (%s) — %s %.0fGi — $%.2f/mo",
				pv.Name, pv.Namespace, pv.StorageClass, pv.CapacityGiB, pv.MonthlyCost)
		}
	}

	// Load Balancers
	if len(report.LBCosts) > 0 {
		summary += "\n\n*Load Balancers:*"
		for _, lb := range report.LBCosts {
			summary += fmt.Sprintf("\n• `%s` (%s) — $%.2f/mo", lb.Name, lb.Namespace, lb.MonthlyCost)
		}
	}

	// Spot readiness
	if len(report.SpotReadiness) > 0 {
		ready := 0
		for _, sr := range report.SpotReadiness {
			if sr.Status == "spot-ready" {
				ready++
			}
		}
		total := len(report.SpotReadiness)
		if report.SpotSavings > 0 {
			summary += fmt.Sprintf("\n\n*Spot Readiness:* %d/%d workloads spot-ready — save $%.2f/mo", ready, total, report.SpotSavings)
		} else {
			summary += fmt.Sprintf("\n\n*Spot Readiness:* %d/%d workloads spot-ready", ready, total)
		}
	}

	summary += fmt.Sprintf("\n\n*Cost Breakdown:*\nCompute: $%.2f | Storage: $%.2f | LB: $%.2f | Network: $%.2f\n*Total: $%.2f/mo*",
		report.MonthlyCost, report.TotalPVCost, report.TotalLBCost, report.TotalNetworkCost, report.TotalMonthlyCost)

	return summary, nil
}

func (s *Server) handleReconcile(ctx context.Context, text string) (string, error) {
	provider := "aws"
	azureSubscription := ""
	days := 7

	args := strings.Fields(text)
	for i, arg := range args {
		if arg == "--provider" && i+1 < len(args) {
			provider = args[i+1]
		}
		if arg == "--azure-subscription" && i+1 < len(args) {
			azureSubscription = args[i+1]
		}
		if arg == "--days" && i+1 < len(args) {
			if d, err := fmt.Sscanf(args[i+1], "%d", &days); err != nil || d != 1 || days < 1 {
				days = 7
			}
		}
	}

	report, err := s.getReport(ctx)
	if err != nil {
		return "", err
	}

	info, err := s.collector.Collect(ctx)
	if err != nil {
		return "", err
	}

	dataDelay := 48 * time.Hour
	if provider == "azure" {
		dataDelay = 24 * time.Hour
	}
	end := time.Now().UTC().Add(-dataDelay)
	start := end.AddDate(0, 0, -days)

	var result *billing.ReconciliationReport

	switch provider {
	case "azure":
		if azureSubscription == "" {
			azureSubscription = os.Getenv("AZURE_SUBSCRIPTION_ID")
		}
		azureCfg := billing.AzureConfig{SubscriptionID: azureSubscription}
		azureClient, err := billing.NewAzureCostClient(ctx, azureCfg)
		if err != nil {
			return fmt.Sprintf("Azure connection failed: %v", err), nil
		}
		estimatedCosts := make(map[string]float64)
		for _, n := range report.Nodes {
			estimatedCosts[n.Name] = n.MonthlyPrice
		}
		result, err = billing.ReconcileAzure(ctx, azureClient, info.Nodes, estimatedCosts, report.Namespaces, start, end, float64(days))
		if err != nil {
			return fmt.Sprintf("Azure cost query failed: %v", err), nil
		}
	default:
		cfg := billing.AthenaConfig{
			Database:       os.Getenv("CUR_DATABASE"),
			Table:          os.Getenv("CUR_TABLE"),
			OutputLocation: os.Getenv("CUR_OUTPUT_LOCATION"),
			WorkGroup:      os.Getenv("CUR_WORKGROUP"),
			Region:         os.Getenv("CUR_REGION"),
		}
		if cfg.WorkGroup == "" {
			cfg.WorkGroup = "primary"
		}

		if err := billing.ValidateAthenaConfig(cfg); err != nil {
			return "CUR not configured. Set CUR_DATABASE, CUR_TABLE, CUR_OUTPUT_LOCATION env vars.", nil
		}

		athenaClient, err := billing.NewAthenaClient(ctx, cfg)
		if err != nil {
			return fmt.Sprintf("Athena connection failed: %v", err), nil
		}

		reconciler := billing.NewReconciler(athenaClient)
		result, err = reconciler.Reconcile(ctx, report, info, start, end)
		if err != nil {
			return fmt.Sprintf("Reconciliation failed: %v", err), nil
		}
	}

	summary := fmt.Sprintf("*Reconciliation (%s - %s)*\n%s\n",
		result.PeriodStart.Format("Jan 2"), result.PeriodEnd.Format("Jan 2, 2006"),
		result.DataDelay)

	summary += "\n*Nodes:*"
	for _, n := range result.Nodes {
		diff := ""
		if n.ActualCost > 0 {
			diff = fmt.Sprintf(" (%+.0f%%)", n.DifferencePercent)
		}
		summary += fmt.Sprintf("\n• `%s` %s — est $%.2f → actual $%.2f%s",
			n.NodeName, n.PricingTerm, n.EstimatedMonthlyCost, n.ActualCost, diff)
	}

	summary += fmt.Sprintf("\n\n*Summary:*\nEstimated: $%.2f/mo\nActual: $%.2f/mo\nDifference: $%+.2f (%+.1f%%)",
		result.TotalEstimatedCost, result.TotalActualCost,
		result.TotalDifference, result.TotalDiffPercent)

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
	result := fmt.Sprintf("*Namespace: %s* (%d pods, $%.2f/mo)\n", ns, len(pods), totalCost)

	for _, p := range pods {
		if hasPrometheus {
			result += fmt.Sprintf("\n• `%s` — $%.2f/mo\n    CPU: %s req → %s used | MEM: %s req → %s used",
				p.Name, p.MonthlyCost,
				output.FormatMillicores(p.CPURequest), output.FormatCores(p.CPUUsage),
				output.FormatBytes(p.MemRequest), output.FormatBytes(p.MemUsage))
		} else {
			result += fmt.Sprintf("\n• `%s` — $%.2f/mo", p.Name, p.MonthlyCost)
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

	report, err := analyzer.New(pp).Analyze(ctx, info)
	if err != nil {
		return nil, err
	}
	report.Period = s.config.Period

	return report, nil
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

	body, err := json.Marshal(payload)
	if err != nil {
		fmt.Printf("failed to marshal slack response: %v\n", err)
		return
	}

	client := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Post(responseURL, "application/json", strings.NewReader(string(body)))
	if err != nil {
		fmt.Printf("failed to send slack response: %v\n", err)
		return
	}
	resp.Body.Close()
}

