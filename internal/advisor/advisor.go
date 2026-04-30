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

func (a *Advisor) Analyze(ctx context.Context, report *analyzer.CostReport) (*Report, error) {
	prompt := buildPrompt(report)

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

const systemPrompt = `You are a Kubernetes FinOps expert. Analyze cluster data and provide actionable recommendations.

CRITICAL: You MUST return 1-3 recommendations in the recommendations array. Never return empty array.

SUMMARY (2 sentences max):
- Key finding: "X of Y nodes are >Z% idle, wasting $W/month"
- Best action briefly

EACH RECOMMENDATION MUST HAVE:
- id: unique (e.g., "spot-1")
- category: "cost"
- severity: "high" for >$100 savings, "medium" otherwise
- title: Action with real node names (e.g., "Convert ip-10-5-10-188, ip-10-5-10-213 to Spot")
- description: Why + risk warning for high severity
- action: Exact command (e.g., "eksctl create nodegroup --cluster=CLUSTER --spot --nodes=5")
- estimated_savings: PRE-CALCULATED value (only for primary recommendation)

RISK WARNINGS (MUST add to description):
- Spot: "⚠️ Only for stateless workloads (Deployments with >1 replica). Do NOT convert StatefulSets, databases, or single-replica services. Spot instances can be interrupted with 2 min notice."
- Consolidation: "⚠️ Test failover first. Check PodDisruptionBudgets."

RULES:
1. Use PRE-CALCULATED SAVINGS from prompt exactly
2. Use REAL node names from data
3. Only ONE recommendation gets estimated_savings
4. Pick ONE strategy: spot OR consolidation (not both)
5. Reference NAMESPACE data: compare costs, identify over-provisioned namespaces, flag dev/qa vs prod imbalances`

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

	savings := CalculateSavings(report, DefaultSavingsConfig())

	savingsInfo := "\n\n---\nPRE-CALCULATED SAVINGS (use these exact values, pick ONE):\n"

	if savings.SpotConversion != nil && savings.SpotConversion.Applicable {
		savingsInfo += fmt.Sprintf("• Spot: up to $%.0f/month (only stateless workloads)\n", savings.SpotConversion.MonthlySavings)
	}
	if savings.NodeConsolidation != nil && savings.NodeConsolidation.Applicable {
		savingsInfo += fmt.Sprintf("• Consolidation: $%.0f/month (remove %s)\n",
			savings.NodeConsolidation.MonthlySavings,
			savings.NodeConsolidation.AffectedNodes[0])
	}
	if savings.RightSizing != nil && savings.RightSizing.Applicable {
		savingsInfo += fmt.Sprintf("• Rightsizing: $%.0f/month\n", savings.RightSizing.MonthlySavings)
	}

	savingsInfo += fmt.Sprintf("\nUse $%.0f for estimated_savings (best option).\n", savings.TotalSavings())

	return fmt.Sprintf("Cluster data:\n%s%s", string(data), savingsInfo)
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
	reportJSON, _ := json.MarshalIndent(report, "", "  ")

	prompt := fmt.Sprintf(`Here is the current Kubernetes cluster cost report:

%s

User question: %s

Answer the question based on the cluster data above. Be specific, use actual node names and numbers from the report. If suggesting actions, include kubectl or eksctl commands. Keep the response concise but informative.`, reportJSON, question)

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
- Do NOT calculate your own values. Use the data as provided.`
