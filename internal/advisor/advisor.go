package advisor

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/tanrikuluozlem/burn/internal/analyzer"
)

type Advisor struct {
	client *anthropic.Client
	model  anthropic.Model
}

func New(apiKey string) *Advisor {
	client := anthropic.NewClient(
		option.WithAPIKey(apiKey),
	)
	return &Advisor{
		client: &client,
		model:  anthropic.ModelClaudeSonnet4_6,
	}
}

func (a *Advisor) Analyze(ctx context.Context, report *analyzer.CostReport, focusNamespace ...string) (*Report, error) {
	prompt := buildPrompt(report)
	if len(focusNamespace) > 0 && focusNamespace[0] != "" {
		ns := focusNamespace[0]
		var nsCost float64
		for _, n := range report.Namespaces {
			if n.Name == ns {
				nsCost = n.MonthlyCost
				break
			}
		}
		var podList string
		for _, p := range report.AllPods {
			if p.Namespace == ns {
				podList += fmt.Sprintf("  - %s (CPU: %dm req, %.2fm used, MEM: %dMi req, %dMi used, $%.2f/mo)\n",
					p.Name, p.CPURequest, p.CPUUsage*1000, p.MemRequest/(1024*1024), p.MemUsage/(1024*1024), p.MonthlyCost)
			}
		}
		prompt += fmt.Sprintf("\n\nFOCUS: Analyze ONLY the '%s' namespace (total cost: $%.2f/mo).\nPods in this namespace:\n%s\nRULES FOR NAMESPACE MODE:\n- ONLY reference pods listed above. Do NOT mention pods from other namespaces.\n- Do NOT recommend node-level changes (spot, consolidation, draining).\n- Do NOT include estimated_savings.\n- Ignore the PRE-CALCULATED SAVINGS section.\n- Use ONLY the pod names listed above in titles and commands.", ns, nsCost, podList)
	}

	tool := anthropic.ToolParam{
		Name:        "provide_recommendations",
		Description: anthropic.String("Provide cost optimization recommendations for the Kubernetes cluster"),
		InputSchema: recommendationSchema,
	}

	resp, err := a.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:       a.model,
		MaxTokens:   4096,
		Temperature: anthropic.Float(0), // Deterministic output
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
		},
		System: []anthropic.TextBlockParam{{Text: systemPrompt}},
		Tools:  []anthropic.ToolUnionParam{{OfTool: &tool}},
		ToolChoice: anthropic.ToolChoiceUnionParam{
			OfTool: &anthropic.ToolChoiceToolParam{Name: "provide_recommendations"},
		},
	})
	if err != nil {
		return nil, err
	}

	recommendations, summary, err := parseToolResponse(resp)
	if err != nil {
		return nil, err
	}

	for i := range recommendations {
		recommendations[i].EstimatedSavings = 0
	}
	savings := CalculateSavings(report, DefaultSavingsConfig())

	var tokensUsed int
	if resp.Usage.InputTokens > 0 {
		tokensUsed = int(resp.Usage.InputTokens + resp.Usage.OutputTokens)
	}

	return &Report{
		Recommendations:       recommendations,
		Summary:               summary,
		TotalPotentialSavings: savings.TotalSavings(),
		SpotSavings:           savings.ApplicableSavings(savings.SpotConversion),
		ConsolidationSavings:  savings.ApplicableSavings(savings.NodeConsolidation),
		RightSizingSavings:    savings.ApplicableSavings(savings.RightSizing),
		GeneratedAt:           time.Now().UTC(),
		ModelUsed:             string(a.model),
		TokensUsed:            tokensUsed,
	}, nil
}

const systemPrompt = `You are a Kubernetes FinOps expert. Analyze cluster data and provide 1-3 actionable recommendations.

Summary: 2 sentences max. Lead with the key finding.

Each recommendation needs: id, category ("cost"), severity, title with real node names, description with risk warning, action as exact command. estimated_savings must be 0 — the engine calculates savings separately.

Risk warnings to include:
- Spot: only for stateless workloads with >1 replica, can be interrupted (AWS 2 min, Azure 30 sec, GCP 30 sec)
- Consolidation: test failover first, verify PodDisruptionBudgets allow voluntary eviction
- PDBs protect against voluntary disruptions (drain, rolling updates) — they do NOT prevent cloud-provider Spot reclamation. Multiple replicas improve resilience but do not guarantee availability during interruption.

Constraints:
- Do not include dollar savings amounts in summary, title, description, or action. The engine displays savings separately.
- Do not rank recommendations by savings amount or claim any is the largest, biggest, or highest financial-impact opportunity. Financial ranking is determined separately by Burn.
- Do not invent thresholds (e.g., "1m minimum", "50m minimum", "7 days required") unless the data provides that evidence.
- Use real node names from data. Do not invent names or numbers.
- Distinguish observation from inference: a low-usage or standalone pod is a candidate for review, not automatically "forgotten", "suspicious", "unused", or "safe to delete".
- An unmatched resource should be investigated, not assumed orphaned or safe to remove.
- A high-idle node is a candidate for review, not automatically safe to drain.
- When p95 data is shown for a pod, recommend request = p95 × 1.5 (50% headroom).
- When no p95 data is shown, observe the inefficiency but do not recommend specific CPU/memory request values.
- Reference namespace data: compare costs, flag dev/qa vs prod imbalances.
- The action field must contain only read-only commands (kubectl get, describe, logs). Do not generate mutating commands (patch, apply, delete, drain, scale, cordon, edit, set) or kubectl top in action, title, description, or summary.
- Describe intended changes in prose without constructing the mutation command.
- kubectl top shows current usage, not historical P95. Do not describe it as a P95 data source.
- Do not assume Spot node groups, labels, or other cluster state that Burn has not verified.
- Title and description values must match. Only use real kubectl flags (e.g., --dry-run=client not --dry-run=true).`

var recommendationSchema = anthropic.ToolInputSchemaParam{
	Type: "object",
	Properties: map[string]any{
		"summary": map[string]any{
			"type":        "string",
			"description": "Brief overview of findings",
		},
		"recommendations": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":       map[string]string{"type": "string"},
					"category": map[string]string{"type": "string"},
					"severity": map[string]string{"type": "string"},
					"title":    map[string]string{"type": "string"},
					"description": map[string]any{
						"type":        "string",
						"description": "What the issue is",
					},
					"action": map[string]any{
						"type":        "string",
						"description": "Read-only verification command (kubectl get, describe, logs). No mutating commands.",
					},
					"estimated_savings": map[string]any{
						"type":        "number",
						"description": "Monthly savings in USD",
					},
					"affected_resources": map[string]any{
						"type":  "array",
						"items": map[string]string{"type": "string"},
					},
				},
				"required": []string{"id", "category", "severity", "title", "description"},
			},
		},
	},
	Required: []string{"summary", "recommendations"},
}

func buildPrompt(report *analyzer.CostReport) string {
	data, _ := json.MarshalIndent(report, "", "  ")

	// Pre-calculated node summary (AI must not calculate these)
	nodeSummary := "\n\n---\nNODE SUMMARY (use these exact values):\n"
	for _, n := range report.Nodes {
		spot := "on-demand"
		if n.IsSpot {
			spot = "spot"
		}
		nodeSummary += fmt.Sprintf("• %s — %s %s — $%.2f/mo — %.0f%% CPU requested — %.0f%% MEM requested — %.0f%% idle — $%.2f/mo idle cost\n",
			n.Name, n.InstanceType, spot, n.MonthlyPrice,
			n.CPURequested*100, n.MemRequested*100, n.IdlePercent*100, n.IdleCostMonthly)
	}

	// Pre-calculated namespace summary
	nsSummary := "\nNAMESPACE SUMMARY (use these exact values):\n"
	for _, ns := range report.Namespaces {
		nsSummary += fmt.Sprintf("• %s — %d pods — $%.2f/mo (CPU: $%.2f, RAM: $%.2f)\n",
			ns.Name, ns.PodCount, ns.MonthlyCost, ns.CPUCost, ns.RAMCost)
	}

	podHeader := "TOP INEFFICIENT PODS (use these exact values):\n"
	if report.Period != "" {
		podHeader = fmt.Sprintf("TOP INEFFICIENT PODS (%s avg, P95 where available):\n", report.Period)
	} else if report.MetricsSource == "prometheus" {
		podHeader = "TOP INEFFICIENT PODS (instant metrics, no P95 — do not recommend specific request values):\n"
	}
	podSummary := "\n" + podHeader
	for _, p := range report.InefficientPods {
		p95Info := ""
		if p.CPUP95Usage > 0 {
			p95Info = fmt.Sprintf(" — CPU p95: %.2fm", p.CPUP95Usage*1000)
		}
		if p.MemoryP95Usage > 0 {
			p95Info += fmt.Sprintf(" — MEM p95: %dMi", p.MemoryP95Usage/(1024*1024))
		}
		podSummary += fmt.Sprintf("• %s/%s — CPU: %dm req, %.2fm used (%.1f%% eff) — MEM: %dMi req, %dMi used (%.0f%% eff)%s — $%.2f/mo\n",
			p.Namespace, p.Name, p.CPURequest, p.CPUUsage*1000, p.CPUEfficiency*100,
			p.MemRequest/(1024*1024), p.MemUsage/(1024*1024), p.MemEfficiency*100,
			p95Info, p.MonthlyCost)
	}

	spotSummary := ""
	if len(report.SpotReadiness) > 0 {
		ready := 0
		for _, s := range report.SpotReadiness {
			if s.Status == "spot-ready" {
				ready++
			}
		}
		spotSummary = fmt.Sprintf("\nSPOT READINESS (%d/%d workloads spot-ready):\n", ready, len(report.SpotReadiness))
		for _, s := range report.SpotReadiness {
			extra := ""
			if s.Status == "spot-ready" && s.Discount > 0 {
				extra = fmt.Sprintf(" — %.0f%% discount (%s)", s.Discount*100, s.PricingSource)
			}
			spotSummary += fmt.Sprintf("• %s/%s (%s, %d replicas) — %s: %s%s\n",
				s.Namespace, s.Name, s.Kind, s.Replicas, s.Status, s.Reason, extra)
		}
	}

	return fmt.Sprintf("Cluster data:\n%s%s%s%s%s", string(data), nodeSummary, nsSummary, podSummary, spotSummary)
}

type toolInput struct {
	Summary         string           `json:"summary"`
	Recommendations []Recommendation `json:"recommendations"`
}

func parseToolResponse(resp *anthropic.Message) ([]Recommendation, string, error) {
	for _, block := range resp.Content {
		if v, ok := block.AsAny().(anthropic.ToolUseBlock); ok {
			var input toolInput
			if err := json.Unmarshal([]byte(v.JSON.Input.Raw()), &input); err != nil {
				return nil, "", err
			}
			return input.Recommendations, input.Summary, nil
		}
	}
	return nil, "", fmt.Errorf("no tool_use block")
}

// Ask answers natural language questions about the cluster costs
func (a *Advisor) Ask(ctx context.Context, report *analyzer.CostReport, question string, extraContext ...string) (string, error) {
	prompt := a.buildAskPrompt(report, question, extraContext...)

	resp, err := a.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     a.model,
		MaxTokens: 1024,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
		},
		System: []anthropic.TextBlockParam{{Text: askSystemPrompt}},
	})
	if err != nil {
		return "", err
	}

	for _, block := range resp.Content {
		if v, ok := block.AsAny().(anthropic.TextBlock); ok {
			return v.Text, nil
		}
	}

	return "", fmt.Errorf("no text response")
}

// AskStream streams the AI response token by token.
func (a *Advisor) AskStream(ctx context.Context, report *analyzer.CostReport, question string, onText func(string), extraContext ...string) (string, error) {
	prompt := a.buildAskPrompt(report, question, extraContext...)

	stream := a.client.Messages.NewStreaming(ctx, anthropic.MessageNewParams{
		Model:     a.model,
		MaxTokens: 4096,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
		},
		System: []anthropic.TextBlockParam{{Text: askSystemPrompt}},
	})

	var full string
	for stream.Next() {
		event := stream.Current()
		delta := event.AsContentBlockDelta()
		if td := delta.Delta.AsTextDelta(); td.Text != "" {
			onText(td.Text)
			full += td.Text
		}
	}
	if err := stream.Err(); err != nil {
		return "", err
	}

	return full, nil
}

func (a *Advisor) buildAskPrompt(report *analyzer.CostReport, question string, extraContext ...string) string {
	reportJSON, _ := json.MarshalIndent(report, "", "  ")
	billing := ""
	if len(extraContext) > 0 && extraContext[0] != "" {
		billing = fmt.Sprintf("\n\nReconciliation data (actual billing from cloud provider — this is the source of truth for SP/RI/Spot pricing):\n%s", extraContext[0])
	}

	// Pre-calculated spot savings so AI doesn't derive its own
	spotSummary := ""
	if len(report.SpotReadiness) > 0 {
		ready := 0
		for _, s := range report.SpotReadiness {
			if s.Status == "spot-ready" {
				ready++
			}
		}
		spotSummary = fmt.Sprintf("\n\nSPOT READINESS (%d/%d workloads spot-ready):\n", ready, len(report.SpotReadiness))
		for _, s := range report.SpotReadiness {
			if s.Status == "spot-ready" {
				spotSummary += fmt.Sprintf("• %s/%s — spot-ready\n", s.Namespace, s.Name)
			}
		}
	}

	return fmt.Sprintf(`Here is the current Kubernetes cluster cost report:

%s%s%s

User question: %s

Answer the question based on the cluster data above. Be specific, use actual node names and numbers from the report. If suggesting actions, include kubectl or eksctl commands. Keep the response concise but informative.`, reportJSON, billing, spotSummary, question)
}

const askSystemPrompt = `You are a Kubernetes FinOps expert assistant. You help users understand their cluster costs and find optimization opportunities.

Guidelines:
- Always respond in English regardless of the question language
- Be conversational but concise
- Use specific data from the cluster report (node names, actual costs, utilization percentages)
- When suggesting actions, provide exact kubectl/eksctl commands as examples for review
- Explain trade-offs (e.g., spot instances are cheaper but can be interrupted)
- If you don't have enough data to answer, say so
- Format numbers clearly ($X.XX for costs, X% for percentages)
- CPU usage in the report is in cores. Convert to millicores for display: 0.005 cores = 5m, 0.0001 cores = <1m
- Do NOT calculate your own values. Use the data as provided. Never sum, multiply, or derive numbers — only quote exact values from the JSON data.
- Do not invent specific CPU/memory request targets unless p95 data is provided for that pod.
- When listing items, COUNT them from the data. Do not guess the count — verify it matches the items you list.
- When showing totals, use ONLY the pre-calculated fields (total_estimated, total_actual, unmatched_compute). Never add up individual line items yourself.
- Distinguish observation from inference: low-usage or standalone pods are candidates for review, not automatically "forgotten" or "safe to delete".
- A high-idle node is a candidate for investigation, not automatically safe to drain or remove. Do not claim a node can be eliminated unless the data contains evidence of remaining-node capacity, scheduling feasibility, and PDB compatibility.
- Idle cost is unallocated capacity cost. It is NOT the same as realizable savings. Do not convert idle cost into a guaranteed cloud-bill reduction.
- Do not claim that over-provisioned pod requests are causing nodes to remain running unless the data establishes that causal relationship (e.g., autoscaler configuration). Node count may be fixed or managed independently.
- PDBs protect against voluntary disruptions only — they do NOT prevent cloud-provider Spot reclamation.
- Do not assume cluster state that is not in the provided data (e.g., existence of Spot node groups, labels, autoscaler config).
- The action field must contain only read-only investigation commands (kubectl get, describe, logs, top). Do not generate mutating commands (patch, apply, delete, drain, scale, cordon, edit, set).
- Only use real kubectl flags. Do NOT invent flags.`
