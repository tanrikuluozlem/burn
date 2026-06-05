package billing

import "time"

type AthenaConfig struct {
	Database       string
	Table          string
	OutputLocation string
	WorkGroup      string
	Region         string
}

type CURLineItem struct {
	ResourceID     string
	UsageType      string
	UsageAmount    float64
	EffectiveCost  float64
	PricingTerm    string
	ReservationARN string
	SavingsPlanARN string
	InstanceType   string
	Region         string
}

type CURColumnSet struct {
	HasReservationARN bool
	HasSavingsPlanARN bool
	HasEffectiveCost  bool
	HasSplitLineItem  bool
}

type AggregatedCost struct {
	ResourceID       string
	TotalCost        float64
	ComputeCost      float64
	DataTransferCost float64
	UsageHours       float64
	PricingTerm      string
	RICost           float64
	SPCost           float64
	SpotCost         float64
	OnDemandCost     float64
}

type NodeReconciliation struct {
	NodeName     string
	InstanceID   string
	InstanceType string
	Region       string
	IsSpot       bool

	EstimatedMonthlyCost float64
	ActualCost           float64
	ActualComputeCost    float64
	ActualTransferCost   float64
	ActualHours          float64
	PricingTerm          string

	CostDifference    float64
	DifferencePercent float64
	MatchMethod       string
	DriftAlert        string // empty = ok, otherwise alert message
}

type NamespaceReconciliation struct {
	Name          string
	EstimatedCost float64
	ActualCost    float64
	Difference    float64
	DiffPercent   float64
	HasSplitCost  bool
}

type ReconciliationReport struct {
	GeneratedAt time.Time
	PeriodStart time.Time
	PeriodEnd   time.Time
	DataDelay   string

	TotalEstimatedCost float64
	TotalActualCost    float64
	TotalDifference    float64
	TotalDiffPercent   float64

	Nodes      []NodeReconciliation
	Namespaces []NamespaceReconciliation

	RINodeCount       int
	SPNodeCount       int
	SpotNodeCount     int
	OnDemandNodeCount int
	TotalRISavings    float64
	TotalSPSavings    float64
	TotalSpotSavings  float64

	UnmatchedCURItems int
	UnmatchedNodes    int
	MissingCURColumns []string
	DaysQueried       int
	DaysFailed        int

	DataScannedBytes int64
}
