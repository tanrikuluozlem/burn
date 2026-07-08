package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tanrikuluozlem/burn/internal/analyzer"
	"github.com/tanrikuluozlem/burn/internal/billing"
	"github.com/tanrikuluozlem/burn/internal/collector"
	"github.com/tanrikuluozlem/burn/internal/pricing"
)

type Config struct {
	Kubeconfig    string
	Kubecontext   string
	PrometheusURL string
	Period        string
}

type Server struct {
	config  Config
	pricing *pricing.CloudPricingProvider
	server  *mcp.Server
}

type AnalyzeInput struct {
	Namespace string `json:"namespace,omitempty" jsonschema:"target namespace, omit for cluster-wide analysis"`
	Period    string `json:"period,omitempty" jsonschema:"analysis period in Prometheus duration format, e.g. 1h 7d 30d"`
}

type SpotInput struct {
	Period string `json:"period,omitempty" jsonschema:"analysis period in Prometheus duration format, e.g. 1h 7d 30d"`
}

type ReconcileInput struct {
	Provider string `json:"provider,omitempty" jsonschema:"cloud provider: aws or azure, auto-detected from cluster if omitted"`
	Days     int    `json:"days,omitempty" jsonschema:"number of days to query, default 7"`
}

func New(cfg Config, pp *pricing.CloudPricingProvider, version string) *Server {
	s := &Server{
		config:  cfg,
		pricing: pp,
	}

	s.server = mcp.NewServer(
		&mcp.Implementation{Name: "burn", Version: version},
		nil,
	)

	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "analyze",
		Description: "Analyze Kubernetes cluster costs by node, namespace, and pod. Returns monthly cost, idle capacity, and resource usage.",
	}, s.handleAnalyze)

	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "spot_readiness",
		Description: "Check which workloads can safely run on spot instances. Evaluates replica count, PDB, local storage, GPU, and priority class.",
	}, s.handleSpotReadiness)

	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "reconcile",
		Description: "Compare estimated Kubernetes costs against actual cloud bill. Supports AWS (CUR/Athena) and Azure (Cost Management). Detects RI/SP/Spot pricing, orphaned disks, and coverage gaps.",
	}, s.handleReconcile)

	return s
}

func (s *Server) Run(ctx context.Context) error {
	return s.server.Run(ctx, &mcp.StdioTransport{})
}

var validPeriod = regexp.MustCompile(`^\d{1,4}[smhdwy]$`)

func (s *Server) collect(period string) (*collector.Collector, error) {
	if period == "" {
		period = s.config.Period
	}
	if period != "" && !validPeriod.MatchString(period) {
		return nil, fmt.Errorf("invalid period %q: use format like 1h, 7d, 30d", period)
	}
	return collector.New(s.config.Kubeconfig, s.config.Kubecontext, "", s.config.PrometheusURL, period)
}

func (s *Server) getReportWithInfo(ctx context.Context, period string) (*analyzer.CostReport, *collector.ClusterInfo, error) {
	coll, err := s.collect(period)
	if err != nil {
		return nil, nil, fmt.Errorf("create collector: %w", err)
	}

	info, err := coll.Collect(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("collect cluster data: %w", err)
	}

	report, err := analyzer.New(s.pricing).Analyze(ctx, info)
	if err != nil {
		return nil, nil, fmt.Errorf("analyze costs: %w", err)
	}
	report.Period = coll.Period()

	return report, info, nil
}

func (s *Server) handleAnalyze(ctx context.Context, _ *mcp.CallToolRequest, input AnalyzeInput) (*mcp.CallToolResult, any, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	report, _, err := s.getReportWithInfo(ctx, input.Period)
	if err != nil {
		return nil, nil, err
	}

	if input.Namespace != "" {
		return namespaceResult(report, input.Namespace)
	}

	return analyzeResult(report)
}

type mcpNamespace struct {
	Name           string  `json:"name"`
	PodCount       int     `json:"pod_count"`
	CPUCoresReq    float64 `json:"cpu_cores_requested"`
	CPUCoresUsed   float64 `json:"cpu_cores_used"`
	MemBytesReq    int64   `json:"mem_bytes_requested"`
	MemBytesUsed   int64   `json:"mem_bytes_used"`
	MonthlyCost    float64 `json:"monthly_cost"`
	CPUCost        float64 `json:"cpu_cost"`
	RAMCost        float64 `json:"ram_cost"`
	StorageCost    float64 `json:"storage_cost,omitempty"`
}

func toMCPNamespaces(namespaces []analyzer.NamespaceCost) []mcpNamespace {
	out := make([]mcpNamespace, len(namespaces))
	for i, ns := range namespaces {
		out[i] = mcpNamespace{
			Name:         ns.Name,
			PodCount:     ns.PodCount,
			CPUCoresReq:  float64(ns.CPURequest) / 1000,
			CPUCoresUsed: ns.CPUUsage,
			MemBytesReq:  ns.MemRequest,
			MemBytesUsed: ns.MemUsage,
			MonthlyCost:  ns.MonthlyCost,
			CPUCost:      ns.CPUCost,
			RAMCost:      ns.RAMCost,
			StorageCost:  ns.StorageCost,
		}
	}
	return out
}

func analyzeResult(report *analyzer.CostReport) (*mcp.CallToolResult, any, error) {
	idlePercent := 0.0
	if report.MonthlyCost > 0 {
		idlePercent = report.TotalIdleCost / report.MonthlyCost * 100
	}

	result := struct {
		TotalNodes       int                    `json:"total_nodes"`
		TotalPods        int                    `json:"total_pods"`
		MonthlyCost      float64                `json:"monthly_cost"`
		TotalIdleCost    float64                `json:"total_idle_cost"`
		IdlePercent      float64                `json:"idle_percent"`
		TotalMonthlyCost float64                `json:"total_monthly_cost"`
		MetricsSource    string                 `json:"metrics_source"`
		Period           string                 `json:"period,omitempty"`
		Nodes            []analyzer.NodeCost    `json:"nodes"`
		Namespaces       []mcpNamespace         `json:"namespaces"`
		PVCosts          []analyzer.PVCost      `json:"pv_costs,omitempty"`
		LBCosts          []analyzer.LBCost      `json:"lb_costs,omitempty"`
		TotalPVCost      float64                `json:"total_pv_cost,omitempty"`
		TotalLBCost      float64                `json:"total_lb_cost,omitempty"`
		WasteAnalysis    analyzer.WasteAnalysis `json:"waste_analysis"`
	}{
		TotalNodes:       report.TotalNodes,
		TotalPods:        report.TotalPods,
		MonthlyCost:      report.MonthlyCost,
		TotalIdleCost:    report.TotalIdleCost,
		IdlePercent:      idlePercent,
		TotalMonthlyCost: report.TotalMonthlyCost,
		MetricsSource:    report.MetricsSource,
		Period:           report.Period,
		Nodes:            report.Nodes,
		Namespaces:       toMCPNamespaces(report.Namespaces),
		PVCosts:          report.PVCosts,
		LBCosts:          report.LBCosts,
		TotalPVCost:      report.TotalPVCost,
		TotalLBCost:      report.TotalLBCost,
		WasteAnalysis:    report.WasteAnalysis,
	}

	data, err := json.Marshal(result)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal report: %w", err)
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(data)}},
	}, nil, nil
}

func namespaceResult(report *analyzer.CostReport, ns string) (*mcp.CallToolResult, any, error) {
	for _, n := range report.Namespaces {
		if n.Name == ns {
			mcpNS := toMCPNamespaces([]analyzer.NamespaceCost{n})[0]
			data, err := json.Marshal(mcpNS)
			if err != nil {
				return nil, nil, fmt.Errorf("marshal namespace: %w", err)
			}
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: string(data)}},
			}, nil, nil
		}
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("namespace %q not found", ns)}},
		IsError: true,
	}, nil, nil
}

func (s *Server) handleSpotReadiness(ctx context.Context, _ *mcp.CallToolRequest, input SpotInput) (*mcp.CallToolResult, any, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	report, _, err := s.getReportWithInfo(ctx, input.Period)
	if err != nil {
		return nil, nil, err
	}
	return spotResult(report)
}

func (s *Server) handleReconcile(ctx context.Context, _ *mcp.CallToolRequest, input ReconcileInput) (*mcp.CallToolResult, any, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()

	report, info, err := s.getReportWithInfo(ctx, "")
	if err != nil {
		return nil, nil, err
	}

	days := input.Days
	if days <= 0 {
		days = 7
	}
	if days > 365 {
		days = 365
	}

	provider := input.Provider
	if provider == "" {
		for _, n := range info.Nodes {
			if n.CloudProvider == collector.CloudAzure {
				provider = "azure"
				break
			}
		}
		if provider == "" {
			provider = "aws"
		}
	}

	dataDelay := 48 * time.Hour
	end := time.Now().UTC().Add(-dataDelay)
	start := end.AddDate(0, 0, -days)

	estimatedCosts, pvEstimates, lbEstimates := billing.BuildEstimateMaps(report)

	var result *billing.ReconciliationReport

	switch provider {
	case "azure":
		sub := os.Getenv("AZURE_SUBSCRIPTION_ID")
		if sub == "" {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "AZURE_SUBSCRIPTION_ID not set"}},
				IsError: true,
			}, nil, nil
		}
		client, err := billing.NewAzureCostClient(ctx, billing.AzureConfig{SubscriptionID: sub, CostType: "amortized"})
		if err != nil {
			return nil, nil, fmt.Errorf("azure client: %w", err)
		}
		result, err = billing.ReconcileAzure(ctx, client, info.Nodes, estimatedCosts, report.Namespaces, info.PVCs, pvEstimates, info.LoadBalancers, lbEstimates, start, end, float64(days))
		if err != nil {
			return nil, nil, fmt.Errorf("azure reconcile: %w", err)
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
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "CUR not configured. Set CUR_DATABASE, CUR_TABLE, CUR_OUTPUT_LOCATION env vars."}},
				IsError: true,
			}, nil, nil
		}
		athenaClient, err := billing.NewAthenaClient(ctx, cfg)
		if err != nil {
			return nil, nil, fmt.Errorf("athena client: %w", err)
		}
		reconciler := billing.NewReconciler(athenaClient)
		result, err = reconciler.Reconcile(ctx, report, info, start, end)
		if err != nil {
			return nil, nil, fmt.Errorf("reconcile: %w", err)
		}
	}

	billing.EnrichCoverageGaps(ctx, result.CoverageGaps, s.pricing)

	wrapper := struct {
		CostBasis string `json:"cost_basis"`
		*billing.ReconciliationReport
	}{
		CostBasis:            "monthly_projected",
		ReconciliationReport: result,
	}

	data, err := json.Marshal(wrapper)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal reconciliation: %w", err)
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(data)}},
	}, nil, nil
}

func spotResult(report *analyzer.CostReport) (*mcp.CallToolResult, any, error) {
	var ready, notReady []analyzer.SpotReadiness
	blockers := map[string]int{}

	for _, w := range report.SpotReadiness {
		if w.Status == "spot-ready" {
			ready = append(ready, w)
		} else {
			notReady = append(notReady, w)
			switch {
			case strings.Contains(w.Reason, "DaemonSet"):
				blockers["daemonset"]++
			case strings.Contains(w.Reason, "StatefulSet"):
				blockers["statefulset"]++
			case strings.Contains(w.Reason, "priority"):
				blockers["cluster_critical"]++
			case strings.Contains(w.Reason, "single replica"):
				blockers["single_replica"]++
			case strings.Contains(w.Reason, "local storage"):
				blockers["local_storage"]++
			case strings.Contains(w.Reason, "GPU"):
				blockers["gpu"]++
			case strings.Contains(w.Reason, "PDB"):
				blockers["pdb_strict"]++
			default:
				blockers["other"]++
			}
		}
	}

	result := struct {
		ReadyCount    int                      `json:"ready_count"`
		NotReadyCount int                      `json:"not_ready_count"`
		Total         int                      `json:"total"`
		Savings       float64                  `json:"potential_savings_monthly"`
		Blockers      map[string]int           `json:"blockers"`
		Ready         []analyzer.SpotReadiness `json:"ready"`
		NotReady      []analyzer.SpotReadiness `json:"not_ready"`
	}{
		ReadyCount:    len(ready),
		NotReadyCount: len(notReady),
		Total:         len(report.SpotReadiness),
		Savings:       report.SpotSavings,
		Blockers:      blockers,
		Ready:         ready,
		NotReady:      notReady,
	}

	data, err := json.Marshal(result)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal spot data: %w", err)
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(data)}},
	}, nil, nil
}
