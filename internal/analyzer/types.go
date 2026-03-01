package analyzer

import "time"

// CostReport is the main output of the analyzer
type CostReport struct {
	GeneratedAt   time.Time
	TotalNodes    int
	TotalPods     int
	SkippedNodes  int
	HourlyCost    float64
	MonthlyCost   float64
	Nodes         []NodeCost
	WasteAnalysis WasteAnalysis
}

// NodeCost represents cost breakdown for a single node
type NodeCost struct {
	Name         string
	InstanceType string
	Region       string
	IsSpot       bool
	HourlyPrice  float64
	MonthlyPrice float64
	PodCount     int
	CPURequested float64 // percentage of allocatable
	MemRequested float64 // percentage of allocatable
	Utilization  float64 // average of CPU and memory
}

// WasteAnalysis identifies underutilized resources
type WasteAnalysis struct {
	UnderutilizedNodes []UnderutilizedNode
	PotentialSavings   float64
}

// UnderutilizedNode represents a node with low resource requests
type UnderutilizedNode struct {
	Name           string
	Utilization    float64
	HourlyCost     float64
	Recommendation string
}
