package billing

import "time"

type AthenaConfig struct {
	Database       string
	Table          string
	OutputLocation string
	WorkGroup      string
	Region         string
}

// AWS billing uses 730 hours/month for cost projection.
const HoursPerMonth = 730.0
const DaysPerMonth = HoursPerMonth / 24.0

// Resource categories for billing classification
const (
	CategoryCompute    = "Compute"
	CategoryDisk       = "Disk"
	CategoryNetwork    = "Network"
	CategoryManagement = "Management"
	CategoryOther      = "Other"
)

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
	Category       string
}

type CURColumnSet struct {
	HasReservationARN bool
	HasSavingsPlanARN bool
	HasEffectiveCost  bool
	HasSplitLineItem  bool
	HasResourceTags   bool // CUR 2.0 MAP column
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
	NodeName     string `json:"node_name"`
	InstanceID   string `json:"instance_id"`
	InstanceType string `json:"instance_type"`
	Region       string `json:"region"`
	IsSpot       bool   `json:"is_spot"`

	EstimatedMonthlyCost float64 `json:"estimated_monthly_cost"`
	ActualCost           float64 `json:"actual_cost"`
	ActualComputeCost    float64 `json:"actual_compute_cost"`
	ActualTransferCost   float64 `json:"actual_transfer_cost"`
	ActualHours          float64 `json:"actual_hours"`
	PricingTerm          string  `json:"pricing_term"`
	OnDemandCost         float64 `json:"on_demand_cost"`
	SpotCost             float64 `json:"spot_cost"`
	RICost               float64 `json:"ri_cost"`
	SPCost               float64 `json:"sp_cost"`

	CostDifference    float64 `json:"cost_difference"`
	DifferencePercent float64 `json:"difference_percent"`
	MatchMethod       string  `json:"match_method"`
	DriftAlert        string  `json:"drift_alert,omitempty"`
}

type NamespaceReconciliation struct {
	Name          string  `json:"name"`
	EstimatedCost float64 `json:"estimated_cost"`
	ActualCost    float64 `json:"actual_cost"`
	Difference    float64 `json:"difference"`
	DiffPercent   float64 `json:"diff_percent"`
	HasSplitCost  bool    `json:"has_split_cost"`
}

type DiskReconciliation struct {
	DiskName      string  `json:"disk_name"`
	PVCName       string  `json:"pvc_name"`
	PVCNamespace  string  `json:"pvc_namespace"`
	StorageClass  string  `json:"storage_class"`
	CapacityGiB   float64 `json:"capacity_gib"`
	EstimatedCost float64 `json:"estimated_cost"`
	ActualCost    float64 `json:"actual_cost"`
	Difference    float64 `json:"difference"`
	DiffPercent   float64 `json:"diff_percent"`
	IsOrphaned    bool    `json:"is_orphaned"`
	MatchMethod   string  `json:"match_method"`
}

type LBReconciliation struct {
	LBName           string  `json:"lb_name"`
	ServiceName      string  `json:"service_name"`
	ServiceNamespace string  `json:"service_namespace"`
	EstimatedCost    float64 `json:"estimated_cost"`
	ActualCost       float64 `json:"actual_cost"`
	Difference       float64 `json:"difference"`
	DiffPercent      float64 `json:"diff_percent"`
	IsOrphaned       bool    `json:"is_orphaned"`
	MatchMethod      string  `json:"match_method"`
}

type PublicIPReconciliation struct {
	Name       string  `json:"name"`
	Address    string  `json:"address,omitempty"`
	ActualCost float64 `json:"actual_cost"`
	AttachedTo string  `json:"attached_to,omitempty"`
}

type CoverageGap struct {
	NodeName        string  `json:"node_name"`
	InstanceType    string  `json:"instance_type"`
	Region          string  `json:"region"`
	MonthlyCost     float64 `json:"monthly_cost"`
	PotentialSaving float64 `json:"potential_saving"`
	Recommendation  string  `json:"recommendation"`
}

type InfrastructureSummary struct {
	ComputeEstimated   float64 `json:"compute_estimated"`
	ComputeActual      float64 `json:"compute_actual"`
	UnmatchedCompute   float64 `json:"unmatched_compute"`
	DiskEstimated      float64 `json:"disk_estimated"`
	DiskActual         float64 `json:"disk_actual"`
	LBEstimated        float64 `json:"lb_estimated"`
	LBActual           float64 `json:"lb_actual"`
	PublicIPActual     float64 `json:"public_ip_actual"`
	ManagementFee      float64 `json:"management_fee"`
	TotalEstimated     float64 `json:"total_estimated"`
	TotalActual        float64 `json:"total_actual"`
}

type ReconciliationReport struct {
	GeneratedAt time.Time `json:"generated_at"`
	PeriodStart time.Time `json:"period_start"`
	PeriodEnd   time.Time `json:"period_end"`
	DataDelay   string    `json:"data_delay"`

	TotalEstimatedCost float64 `json:"total_estimated_cost"`
	TotalActualCost    float64 `json:"total_actual_cost"`
	TotalDifference    float64 `json:"total_difference"`
	TotalDiffPercent   float64 `json:"total_diff_percent"`

	Nodes      []NodeReconciliation      `json:"nodes"`
	Namespaces []NamespaceReconciliation  `json:"namespaces"`

	RINodeCount       int     `json:"ri_node_count"`
	SPNodeCount       int     `json:"sp_node_count"`
	SpotNodeCount     int     `json:"spot_node_count"`
	OnDemandNodeCount int     `json:"on_demand_node_count"`
	TotalRISavings    float64 `json:"total_ri_savings"`
	TotalSPSavings    float64 `json:"total_sp_savings"`
	TotalSpotSavings  float64 `json:"total_spot_savings"`

	UnmatchedCURItems int      `json:"unmatched_cur_items"`
	UnmatchedNodes    int      `json:"unmatched_nodes"`
	MissingCURColumns []string `json:"missing_cur_columns,omitempty"`
	DaysQueried       int      `json:"days_queried"`
	DaysFailed        int      `json:"days_failed"`

	DataScannedBytes int64 `json:"data_scanned_bytes"`

	Disks         []DiskReconciliation    `json:"disks,omitempty"`
	OrphanedDisks []DiskReconciliation    `json:"orphaned_disks,omitempty"`
	LoadBalancers []LBReconciliation      `json:"load_balancers,omitempty"`
	PublicIPs     []PublicIPReconciliation `json:"public_ips,omitempty"`
	CoverageGaps  []CoverageGap           `json:"coverage_gaps,omitempty"`
	InfraCost     *InfrastructureSummary   `json:"infra_cost,omitempty"`
	Warnings      []string                `json:"warnings,omitempty"`
}
