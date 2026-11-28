package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"github.com/tanrikuluozlem/burn/internal/advisor"
	"github.com/tanrikuluozlem/burn/internal/analyzer"
	"github.com/tanrikuluozlem/burn/internal/billing"
	"github.com/tanrikuluozlem/burn/internal/collector"
	"github.com/tanrikuluozlem/burn/internal/plan"
	"github.com/tanrikuluozlem/burn/internal/pricing"
)

const (
	toolAnalyze   = "analyze"
	toolSpot      = "spot_readiness"
	toolReconcile = "reconcile"
	toolWorkloads = plan.WorkloadsTool

	defaultReconcileDays = 7
	maxReconcileDays     = 365
)

var validPeriod = regexp.MustCompile(`^\d{1,4}[smhdwy]$`)

var _ Investigator = (*Tools)(nil)

type Config struct {
	Kubeconfig     string
	Kubecontext    string
	PrometheusURL  string
	Period         string
	DataDelayHours int
	Athena         billing.AthenaConfig
	Azure          billing.AzureConfig
}

type Tools struct {
	cfg     Config
	pricing *pricing.CloudPricingProvider
}

func NewTools(cfg Config, pp *pricing.CloudPricingProvider) *Tools {
	return &Tools{cfg: cfg, pricing: pp}
}

func (t *Tools) Analyze(ctx context.Context, in AnalyzeInput) ([]plan.Evidence, error) {
	report, info, err := t.report(ctx, in.Namespace, in.Period)
	if err != nil {
		return nil, err
	}

	calculated := advisor.CalculateSavings(report, advisor.DefaultSavingsConfig())

	cost, err := costEvidence(report)
	if err != nil {
		return nil, err
	}
	savings, err := savingsEvidence(calculated)
	if err != nil {
		return nil, err
	}
	workloads, err := buildWorkloadEvidence(report, info, calculated, t.periodFor(in.Period))
	if err != nil {
		return nil, err
	}
	return []plan.Evidence{cost, savings, workloads}, nil
}

func (t *Tools) SpotReadiness(ctx context.Context, in SpotInput) ([]plan.Evidence, error) {
	report, _, err := t.report(ctx, "", in.Period)
	if err != nil {
		return nil, err
	}

	e, err := newEvidence(toolSpot, struct {
		Workloads []analyzer.SpotReadiness `json:"workloads"`
		Savings   float64                  `json:"potential_savings_monthly"`
	}{report.SpotReadiness, report.SpotSavings})
	if err != nil {
		return nil, err
	}
	return []plan.Evidence{e}, nil
}

func (t *Tools) Reconcile(ctx context.Context, in ReconcileInput) ([]plan.Evidence, error) {
	days := in.Days
	if days <= 0 {
		days = defaultReconcileDays
	}
	if days > maxReconcileDays {
		days = maxReconcileDays
	}

	if in.Provider != "" {
		if err := t.checkBillingConfig(in.Provider); err != nil {
			return nil, err
		}
	}

	report, info, err := t.report(ctx, "", "")
	if err != nil {
		return nil, err
	}

	provider := in.Provider
	if provider == "" {
		provider = detectProvider(info.Nodes)
		if err := t.checkBillingConfig(provider); err != nil {
			return nil, err
		}
	}

	delay := time.Duration(t.cfg.DataDelayHours) * time.Hour
	end := time.Now().UTC().Add(-delay)
	start := end.AddDate(0, 0, -days)

	var result *billing.ReconciliationReport
	switch provider {
	case "azure":
		client, err := billing.NewAzureCostClient(ctx, t.cfg.Azure)
		if err != nil {
			return nil, fmt.Errorf("azure client: %w", err)
		}
		estimated, pvEstimates, lbEstimates := billing.BuildEstimateMaps(report)
		result, err = billing.ReconcileAzure(ctx, client, info.Nodes, estimated, report.Namespaces,
			info.PVCs, pvEstimates, info.LoadBalancers, lbEstimates, start, end, float64(days))
		if err != nil {
			return nil, fmt.Errorf("azure reconcile: %w", err)
		}
	default:
		client, err := billing.NewAthenaClient(ctx, t.cfg.Athena)
		if err != nil {
			return nil, fmt.Errorf("athena client: %w", err)
		}
		result, err = billing.NewReconciler(client).Reconcile(ctx, report, info, start, end)
		if err != nil {
			return nil, fmt.Errorf("reconcile: %w", err)
		}
	}

	billing.EnrichCoverageGaps(ctx, result.CoverageGaps, t.pricing)

	e, err := newEvidence(toolReconcile, result)
	if err != nil {
		return nil, err
	}
	return []plan.Evidence{e}, nil
}

func (t *Tools) checkBillingConfig(provider string) error {
	if provider == "azure" {
		if t.cfg.Azure.SubscriptionID == "" {
			return fmt.Errorf("%w: azure subscription not set", ErrUnavailable)
		}
		return nil
	}
	if err := billing.ValidateAthenaConfig(t.cfg.Athena); err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return nil
}

func (t *Tools) periodFor(period string) string {
	if period == "" {
		return t.cfg.Period
	}
	return period
}

func (t *Tools) report(ctx context.Context, namespace, period string) (*analyzer.CostReport, *collector.ClusterInfo, error) {
	period = t.periodFor(period)
	if period != "" && !validPeriod.MatchString(period) {
		return nil, nil, fmt.Errorf("invalid period %q: use format like 1h, 7d, 30d", period)
	}

	coll, err := collector.New(t.cfg.Kubeconfig, t.cfg.Kubecontext, namespace, t.cfg.PrometheusURL, period)
	if err != nil {
		return nil, nil, fmt.Errorf("create collector: %w", err)
	}
	info, err := coll.Collect(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("collect cluster data: %w", err)
	}
	report, err := analyzer.New(t.pricing).Analyze(ctx, info)
	if err != nil {
		return nil, nil, fmt.Errorf("analyze costs: %w", err)
	}
	report.Period = coll.Period()

	return report, info, nil
}

func detectProvider(nodes []collector.NodeInfo) string {
	for _, n := range nodes {
		if n.CloudProvider == collector.CloudAzure {
			return "azure"
		}
	}
	return "aws"
}

func costEvidence(report *analyzer.CostReport) (plan.Evidence, error) {
	return newEvidence(toolAnalyze, report)
}

// Per-strategy values use the same applicability rules as advisor.Report.
func savingsEvidence(s *advisor.PotentialSavings) (plan.Evidence, error) {
	return newEvidence(plan.SavingsTool, struct {
		Total         float64 `json:"total"`
		Spot          float64 `json:"spot"`
		Consolidation float64 `json:"consolidation"`
		RightSizing   float64 `json:"right_sizing"`
	}{
		Total:         s.TotalSavings(),
		Spot:          s.ApplicableSavings(s.SpotConversion),
		Consolidation: s.ApplicableSavings(s.NodeConsolidation),
		RightSizing:   s.ApplicableSavings(s.RightSizing),
	})
}

func newEvidence(tool string, payload any) (plan.Evidence, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return plan.Evidence{}, fmt.Errorf("encode %s evidence: %w", tool, err)
	}

	return plan.Evidence{
		ID:   plan.EvidenceID(tool, data),
		Tool: tool,
		Data: data,
	}, nil
}
