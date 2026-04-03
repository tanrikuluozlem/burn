package advisor

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/ozlemtanrikulu/burn/internal/analyzer"
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
		model:  anthropic.ModelClaudeSonnet4_20250514,
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
		Model:     a.model,
		MaxTokens: 2048,
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

const systemPrompt = `You are a Kubernetes FinOps expert. Analyze cluster cost data and provide actionable recommendations.

Focus on:
- Underutilized nodes that could be downsized or removed
- On-demand instances that could be spot instances
- Right-sizing opportunities
- Consolidation possibilities

Be specific. Include kubectl or terraform commands when relevant.`

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
	return fmt.Sprintf("Analyze this Kubernetes cluster cost report and provide recommendations:\n\n%s", string(data))
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
