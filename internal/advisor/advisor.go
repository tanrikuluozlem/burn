package advisor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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

	resp, err := a.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     a.model,
		MaxTokens: 2048,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(
				anthropic.NewTextBlock(prompt),
			),
		},
		System: []anthropic.TextBlockParam{
			{Text: systemPrompt},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("claude api error: %w", err)
	}

	recommendations, summary, err := parseResponse(resp)
	if err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
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

Your response must be valid JSON with this structure:
{
  "summary": "Brief overview of findings",
  "recommendations": [
    {
      "id": "rec-1",
      "category": "cost|performance|reliability",
      "severity": "critical|high|medium|low",
      "title": "Short title",
      "description": "What the issue is",
      "action": "Specific command or step to fix it",
      "estimated_savings": 123.45,
      "affected_resources": ["node-1", "node-2"]
    }
  ]
}

Focus on:
- Underutilized nodes that could be downsized or removed
- On-demand instances that could be spot instances
- Right-sizing opportunities
- Consolidation possibilities

Be specific. Include kubectl or terraform commands when relevant.`

func buildPrompt(report *analyzer.CostReport) string {
	data, _ := json.MarshalIndent(report, "", "  ")
	return fmt.Sprintf("Analyze this Kubernetes cluster cost report and provide recommendations:\n\n%s", string(data))
}

type aiResponse struct {
	Summary         string           `json:"summary"`
	Recommendations []Recommendation `json:"recommendations"`
}

func parseResponse(resp *anthropic.Message) ([]Recommendation, string, error) {
	if len(resp.Content) == 0 {
		return nil, "", fmt.Errorf("empty response")
	}

	text := resp.Content[0].Text
	if text == "" {
		return nil, "", fmt.Errorf("no text in response")
	}

	// extract JSON from markdown code block if present
	text = extractJSON(text)

	var result aiResponse
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		return nil, "", fmt.Errorf("invalid json: %w", err)
	}

	return result.Recommendations, result.Summary, nil
}

func extractJSON(s string) string {
	// remove markdown code blocks
	if idx := strings.Index(s, "```json"); idx != -1 {
		s = s[idx+7:]
	} else if idx := strings.Index(s, "```"); idx != -1 {
		s = s[idx+3:]
	}
	if idx := strings.LastIndex(s, "```"); idx != -1 {
		s = s[:idx]
	}
	return strings.TrimSpace(s)
}
