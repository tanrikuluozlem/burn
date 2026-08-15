package server

import "testing"

func TestParseReconcileArgs_Defaults(t *testing.T) {
	ra := parseReconcileArgs("")
	if ra.provider != "aws" {
		t.Errorf("provider = %q, want aws", ra.provider)
	}
	if ra.days != 7 {
		t.Errorf("days = %d, want 7", ra.days)
	}
	if ra.dataDelayHours != 48 {
		t.Errorf("dataDelayHours = %d, want 48", ra.dataDelayHours)
	}
	if ra.costType != "amortized" {
		t.Errorf("costType = %q, want amortized", ra.costType)
	}
}

func TestParseReconcileArgs_DaysBounds(t *testing.T) {
	tests := []struct {
		name string
		text string
		want int
	}{
		{"no --days", "reconcile --provider aws", 7},
		{"--days 0", "reconcile --days 0", 7},
		{"--days 1", "reconcile --days 1", 1},
		{"--days 30", "reconcile --days 30", 30},
		{"--days 365", "reconcile --days 365", 365},
		{"--days 366 clamped", "reconcile --days 366", 365},
		{"--days 9999 clamped", "reconcile --days 9999", 365},
		{"--days negative", "reconcile --days -1", 7},
		{"--days invalid", "reconcile --days abc", 7},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ra := parseReconcileArgs(tt.text)
			if ra.days != tt.want {
				t.Errorf("days = %d, want %d", ra.days, tt.want)
			}
		})
	}
}

func TestParseReconcileArgs_NoAzureSubscriptionOverride(t *testing.T) {
	ra := parseReconcileArgs("reconcile --provider azure --azure-subscription SOME_OTHER_SUB")
	if ra.provider != "azure" {
		t.Errorf("provider = %q, want azure", ra.provider)
	}
}

func TestParseReconcileArgs_Provider(t *testing.T) {
	ra := parseReconcileArgs("reconcile --provider azure")
	if ra.provider != "azure" {
		t.Errorf("provider = %q, want azure", ra.provider)
	}
}

func TestParseReconcileArgs_CostType(t *testing.T) {
	ra := parseReconcileArgs("reconcile --provider azure --cost-type actual")
	if ra.costType != "actual" {
		t.Errorf("costType = %q, want actual", ra.costType)
	}
}

func TestParseReconcileArgs_DataDelay(t *testing.T) {
	ra := parseReconcileArgs("reconcile --data-delay 24")
	if ra.dataDelayHours != 24 {
		t.Errorf("dataDelayHours = %d, want 24", ra.dataDelayHours)
	}
}
