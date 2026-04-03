package collector

type CloudProvider string

const (
	CloudAWS     CloudProvider = "aws"
	CloudGCP     CloudProvider = "gcp"
	CloudAzure   CloudProvider = "azure"
	CloudUnknown CloudProvider = "unknown"
)

type NodeInfo struct {
	Name           string
	InstanceType   string
	Region         string
	Zone           string
	CloudProvider  CloudProvider
	CPUCores       int64
	MemoryBytes    int64
	CPUAllocatable int64
	MemAllocatable int64
	CPUUsage       float64
	MemoryUsage    int64
	IsSpot         bool
	Labels         map[string]string
	Pods           []PodInfo
}

type PodInfo struct {
	Name          string
	Namespace     string
	CPURequest    int64
	CPULimit      int64
	MemoryRequest int64
	MemoryLimit   int64
	CPUUsage      float64
	MemoryUsage   int64
}

type ClusterInfo struct {
	Nodes      []NodeInfo
	TotalNodes int
	TotalPods  int
}
