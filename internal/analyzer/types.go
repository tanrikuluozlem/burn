package analyzer

import "time"

type CostReport struct {
	GeneratedAt     time.Time
	TotalNodes      int
	TotalPods       int
	SkippedNodes    int
	HourlyCost      float64
	MonthlyCost     float64 // compute only
	TotalIdleCost   float64 // monthly idle cost
	Nodes           []NodeCost
	InefficientPods []PodEfficiency
	Namespaces      []NamespaceCost   `json:"namespaces,omitempty"`
	AllPods         []PodEfficiency   `json:"-"`
	WasteAnalysis   WasteAnalysis
	MetricsSource   string // "prometheus" or "requests"
	Period          string `json:"period,omitempty"`

	// Non-compute costs
	PVCosts          []PVCost     `json:"pv_costs,omitempty"`
	LBCosts          []LBCost     `json:"lb_costs,omitempty"`
	NetworkCost      NetworkCost  `json:"network_cost,omitempty"`
	TotalPVCost      float64      `json:"total_pv_cost,omitempty"`
	TotalLBCost      float64      `json:"total_lb_cost,omitempty"`
	TotalNetworkCost float64      `json:"total_network_cost,omitempty"`
	TotalMonthlyCost float64      `json:"total_monthly_cost"` // compute + storage + LB + network
}

type NodeCost struct {
	Name         string
	InstanceType string
	Region       string
	IsSpot       bool
	HourlyPrice  float64
	MonthlyPrice float64
	PodCount     int

	// Per-resource cost rates
	CPUCostPerCore float64 `json:"cpu_cost_per_core,omitempty"`
	RAMCostPerGiB  float64 `json:"ram_cost_per_gib,omitempty"`
	GPUCostPerUnit float64 `json:"gpu_cost_per_unit,omitempty"`
	GPUCount       int64   `json:"gpu_count,omitempty"`

	// Resource allocation (what pods requested as % of node capacity)
	CPURequested float64
	MemRequested float64

	// Idle capacity cost (per-resource: CPU idle + RAM idle)
	IdleCostHourly  float64
	IdleCostMonthly float64
	IdlePercent     float64 // percentage of node capacity that is idle
	CPUIdleCost     float64 `json:"cpu_idle_cost,omitempty"` // monthly
	RAMIdleCost     float64 `json:"ram_idle_cost,omitempty"` // monthly
}

type PodEfficiency struct {
	Name          string
	Namespace     string
	CPURequest    int64   // millicores
	CPUUsage      float64 // cores (from Prometheus)
	CPUEfficiency float64 // usage/request ratio (0-1+)
	MemRequest    int64   // bytes
	MemUsage      int64   // bytes (from Prometheus)
	MemEfficiency float64 // usage/request ratio (0-1+)
	MonthlyCost    float64 // estimated cost based on resource allocation
	CPUCost        float64 `json:"cpu_cost"`
	RAMCost        float64 `json:"ram_cost"`
	GPUCost        float64 `json:"gpu_cost,omitempty"`
	GPURequest     int64   `json:"gpu_request,omitempty"`
	CPUP95Usage    float64 `json:"cpu_p95_usage,omitempty"`   // p95 CPU usage in cores
	MemoryP95Usage int64   `json:"mem_p95_usage,omitempty"`   // p95 memory usage in bytes
}

type NamespaceCost struct {
	Name        string  `json:"name"`
	PodCount    int     `json:"pod_count"`
	CPURequest  int64   `json:"cpu_request"` // total millicores
	CPUUsage    float64 `json:"cpu_usage"`   // total cores
	MemRequest  int64   `json:"mem_request"` // total bytes
	MemUsage    int64   `json:"mem_usage"`   // total bytes
	MonthlyCost float64 `json:"monthly_cost"`
	CPUCost     float64 `json:"cpu_cost"`
	RAMCost     float64 `json:"ram_cost"`
	StorageCost float64 `json:"storage_cost,omitempty"`
}

type PVCost struct {
	Name         string  `json:"name"`
	Namespace    string  `json:"namespace"`
	StorageClass string  `json:"storage_class"`
	CapacityGiB  float64 `json:"capacity_gib"`
	MonthlyCost  float64 `json:"monthly_cost"`
}

type LBCost struct {
	Name        string  `json:"name"`
	Namespace   string  `json:"namespace"`
	MonthlyCost float64 `json:"monthly_cost"`
}

type NetworkCost struct {
	EgressGiBPerMonth float64 `json:"egress_gib_per_month"`
	MonthlyCost       float64 `json:"monthly_cost"`
}

type WasteAnalysis struct {
	UnderutilizedNodes []UnderutilizedNode
	PotentialSavings   float64
}

type UnderutilizedNode struct {
	Name           string
	IdlePercent    float64
	IdleCost       float64
	Recommendation string
}
