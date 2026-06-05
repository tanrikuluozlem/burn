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

	var totalSavings float64
	for _, r := range recommendations {
		totalSavings += r.EstimatedSavings
	}

	var tokensUsed int
	if resp.Usage.InputTokens > 0 {
		tokensUsed = int(resp.Usage.InputTokens + resp.Usage.OutputTokens)
	}

	return &Report{
		Recommendations:       recommendations,
		Summary:               summary,
		TotalPotentialSavings: totalSavings,
		GeneratedAt:           time.Now().UTC(),
		ModelUsed:             string(a.model),
		TokensUsed:            tokensUsed,
	}, nil
}

const systemPrompt = `You are a Kubernetes FinOps expert. Analyze cluster data and provide 1-3 actionable recommendations.

Summary: 2 sentences max. Lead with the key finding and dollar impact.

Each recommendation needs: id, category ("cost"), severity ("high" if >$100 savings), title with real node names, description with risk warning, action as exact command, estimated_savings (only on primary recommendation).

Risk warnings to include:
- Spot: only for stateless workloads with >1 replica, can be interrupted (AWS 2 min, Azure 30 sec, GCP 30 sec)
- Consolidation: test failover first, check PodDisruptionBudgets

Constraints:
- Use the pre-calculated savings value from the prompt exactly. Do not calculate your own savings.
- Use real node names from data. Do not invent names or numbers.
- Pick one strategy: spot or consolidation, not both.
- Reference namespace data: compare costs, flag dev/qa vs prod imbalances.
- When p95 data is available, recommend request = p95 × 1.5 (50% headroom).
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
						"description": "Specific command or step to fix",
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

	// Pre-calculated top inefficient pods with p95 data
	podSummary := "\nTOP INEFFICIENT PODS (use these exact values):\n"
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

	savings := CalculateSavings(report, DefaultSavingsConfig())

	savingsInfo := "\nPRE-CALCULATED SAVINGS (use these exact values, pick ONE):\n"

	if savings.SpotConversion != nil && savings.SpotConversion.Applicable {
		savingsInfo += fmt.Sprintf("• Spot: up to $%.2f/month (only stateless workloads)\n", savings.SpotConversion.MonthlySavings)
	}
	if savings.NodeConsolidation != nil && savings.NodeConsolidation.Applicable && len(savings.NodeConsolidation.AffectedNodes) > 0 {
		savingsInfo += fmt.Sprintf("• Consolidation: $%.2f/month (remove %s)\n",
			savings.NodeConsolidation.MonthlySavings,
			savings.NodeConsolidation.AffectedNodes[0])
	}
	if savings.RightSizing != nil && savings.RightSizing.Applicable {
		savingsInfo += fmt.Sprintf("• Rightsizing: $%.2f/month\n", savings.RightSizing.MonthlySavings)
	}

	savingsInfo += fmt.Sprintf("\nUse $%.2f for estimated_savings (best option).\n", savings.TotalSavings())

	// Spot readiness summary for AI
	spotSummary := ""
	if len(report.SpotReadiness) > 0 {
		ready := 0
		for _, s := range report.SpotReadiness {
			if s.Status == "spot-ready" {
				ready++
			}
		}
		spotSummary = fmt.Sprintf("\nSPOT READINESS (%d/%d workloads spot-ready, potential savings $%.2f/mo):\n",
			ready, len(report.SpotReadiness), report.SpotSavings)
		for _, s := range report.SpotReadiness {
			extra := ""
			if s.Status == "spot-ready" && s.Discount > 0 {
				extra = fmt.Sprintf(" — %.0f%% discount (%s)", s.Discount*100, s.PricingSource)
			}
			spotSummary += fmt.Sprintf("• %s/%s (%s, %d replicas) — %s: %s%s\n",
				s.Namespace, s.Name, s.Kind, s.Replicas, s.Status, s.Reason, extra)
		}
	}

	return fmt.Sprintf("Cluster data:\n%s%s%s%s%s%s", string(data), nodeSummary, nsSummary, podSummary, savingsInfo, spotSummary)
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
func (a *Advisor) Ask(ctx context.Context, report *analyzer.CostReport, question string) (string, error) {
	prompt := a.buildAskPrompt(report, question)

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
func (a *Advisor) AskStream(ctx context.Context, report *analyzer.CostReport, question string, onText func(string)) (string, error) {
	prompt := a.buildAskPrompt(report, question)

	stream := a.client.Messages.NewStreaming(ctx, anthropic.MessageNewParams{
		Model:     a.model,
		MaxTokens: 1024,
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

func (a *Advisor) buildAskPrompt(report *analyzer.CostReport, question string) string {
	reportJSON, _ := json.MarshalIndent(report, "", "  ")
	return fmt.Sprintf(`Here is the current Kubernetes cluster cost report:

%s

User question: %s

Answer the question based on the cluster data above. Be specific, use actual node names and numbers from the report. If suggesting actions, include kubectl or eksctl commands. Keep the response concise but informative.`, reportJSON, question)
}

const askSystemPrompt = `You are a Kubernetes FinOps expert assistant. You help users understand their cluster costs and find optimization opportunities.

Guidelines:
- Always respond in English regardless of the question language
- Be conversational but concise
- Use specific data from the cluster report (node names, actual costs, utilization percentages)
- When suggesting actions, provide exact kubectl/eksctl commands
- Explain trade-offs (e.g., spot instances are cheaper but can be interrupted)
- If you don't have enough data to answer, say so
- Format numbers clearly ($X.XX for costs, X% for percentages)
- CPU usage in the report is in cores. Convert to millicores for display: 0.005 cores = 5m, 0.0001 cores = <1m
- Do NOT calculate your own values. Use the data as provided.
- When listing items, COUNT them from the data. Do not guess the count — verify it matches the items you list.
- Only use real kubectl flags. Do NOT invent flags.`
