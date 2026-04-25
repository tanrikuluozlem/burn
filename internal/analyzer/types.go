package analyzer

import "time"

type CostReport struct {
	GeneratedAt     time.Time
	TotalNodes      int
	TotalPods       int
	SkippedNodes    int
	HourlyCost      float64
	MonthlyCost     float64
	TotalIdleCost   float64 // monthly idle cost
	Nodes           []NodeCost
	InefficientPods []PodEfficiency
	Namespaces      []NamespaceCost   `json:"namespaces,omitempty"`
	AllPods         []PodEfficiency   `json:"-"`
	WasteAnalysis   WasteAnalysis
	MetricsSource   string // "prometheus" or "requests"
}

type NodeCost struct {
	Name         string
	InstanceType string
	Region       string
	IsSpot       bool
	HourlyPrice  float64
	MonthlyPrice float64
	PodCount     int

	// Resource allocation (what pods requested as % of node capacity)
	CPURequested float64
	MemRequested float64

	// Idle capacity cost (unused capacity in $)
	IdleCostHourly  float64
	IdleCostMonthly float64
	IdlePercent     float64 // percentage of node capacity that is idle
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
	MonthlyCost   float64 // estimated cost based on requests
}

type NamespaceCost struct {
	Name       string  `json:"name"`
	PodCount   int     `json:"pod_count"`
	CPURequest int64   `json:"cpu_request"` // total millicores
	CPUUsage   float64 `json:"cpu_usage"`   // total cores
	MemRequest int64   `json:"mem_request"` // total bytes
	MemUsage   int64   `json:"mem_usage"`   // total bytes
	MonthlyCost float64 `json:"monthly_cost"`
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
