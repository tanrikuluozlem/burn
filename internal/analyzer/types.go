package analyzer

import "time"

type CostReport struct {
	GeneratedAt      time.Time
	TotalNodes       int
	TotalPods        int
	SkippedNodes     int
	HourlyCost       float64
	MonthlyCost      float64
	Nodes            []NodeCost
	WasteAnalysis    WasteAnalysis
	MetricsSource    string // "prometheus" or "requests"
}

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

type WasteAnalysis struct {
	UnderutilizedNodes []UnderutilizedNode
	PotentialSavings   float64
}

type UnderutilizedNode struct {
	Name           string
	Utilization    float64
	HourlyCost     float64
	Recommendation string
}
