package collector

// CloudProvider represents the cloud platform
type CloudProvider string

const (
	CloudAWS     CloudProvider = "aws"
	CloudGCP     CloudProvider = "gcp"
	CloudAzure   CloudProvider = "azure"
	CloudUnknown CloudProvider = "unknown"
)

// NodeInfo holds compute and cost-relevant data for a single node
type NodeInfo struct {
	Name            string
	InstanceType    string
	Region          string
	Zone            string
	CloudProvider   CloudProvider
	CPUCores        int64
	MemoryBytes     int64
	CPUAllocatable  int64
	MemAllocatable  int64
	IsSpot          bool
	Labels          map[string]string
	Pods            []PodInfo
}

// PodInfo holds resource requests and limits for a single pod
type PodInfo struct {
	Name          string
	Namespace     string
	CPURequest    int64
	CPULimit      int64
	MemoryRequest int64
	MemoryLimit   int64
}

// ClusterInfo aggregates all collected data
type ClusterInfo struct {
	Nodes      []NodeInfo
	TotalNodes int
	TotalPods  int
}
