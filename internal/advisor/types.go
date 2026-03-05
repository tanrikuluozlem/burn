package advisor

import "time"

type Category string

const (
	CategoryCost        Category = "cost"
	CategoryPerformance Category = "performance"
	CategoryReliability Category = "reliability"
)

type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityHigh     Severity = "high"
	SeverityMedium   Severity = "medium"
	SeverityLow      Severity = "low"
)

type Recommendation struct {
	ID                string   `json:"id"`
	Category          Category `json:"category"`
	Severity          Severity `json:"severity"`
	Title             string   `json:"title"`
	Description       string   `json:"description"`
	Action            string   `json:"action"`
	EstimatedSavings  float64  `json:"estimated_savings"`
	AffectedResources []string `json:"affected_resources"`
}

type Report struct {
	Recommendations       []Recommendation `json:"recommendations"`
	Summary               string           `json:"summary"`
	TotalPotentialSavings float64          `json:"total_potential_savings"`
	GeneratedAt           time.Time        `json:"generated_at"`
	ModelUsed             string           `json:"model_used"`
	TokensUsed            int              `json:"tokens_used"`
}
