package agent

import (
	"context"
	"errors"

	"github.com/tanrikuluozlem/burn/internal/plan"
)

// ErrUnavailable reports that the tool's integration is not configured.
var ErrUnavailable = errors.New("tool unavailable")

type AnalyzeInput struct {
	Namespace string
	Period    string
}

type SpotInput struct {
	Period string
}

type ReconcileInput struct {
	Provider string
	Days     int
}

type Investigator interface {
	Analyze(ctx context.Context, in AnalyzeInput) ([]plan.Evidence, error)
	SpotReadiness(ctx context.Context, in SpotInput) ([]plan.Evidence, error)
	Reconcile(ctx context.Context, in ReconcileInput) ([]plan.Evidence, error)
}
