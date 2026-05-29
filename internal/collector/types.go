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
	InternalIP     string
	InstanceType   string
	Region         string
	Zone           string
	CloudProvider  CloudProvider
	CPUCores       int64
	MemoryBytes    int64
	CPUAllocatable int64
	MemAllocatable int64
	CPUUsage              float64
	MemoryUsage           int64
	NetworkEgressBytesPerSec float64
	GPUCount              int64
	GPUType               string // e.g. "Tesla-T4", "A100"
	IsSpot                bool
	ProviderID            string
	Labels         map[string]string
	Pods           []PodInfo
}

type PodInfo struct {
	Name           string
	Namespace      string
	CPURequest     int64
	CPULimit       int64
	MemoryRequest  int64
	MemoryLimit    int64
	GPURequest     int64 // nvidia.com/gpu
	CPUUsage       float64
	MemoryUsage    int64
	CPUP95Usage    float64 // p95 CPU usage in cores (over analysis period)
	MemoryP95Usage int64   // p95 memory usage in bytes (over analysis period)
}

type PVCInfo struct {
	Name           string
	Namespace      string
	StorageClass   string
	RequestedBytes int64
	VolumeName     string
}

type LBServiceInfo struct {
	Name      string
	Namespace string
}

type WorkloadInfo struct {
	Name               string
	Namespace          string
	Kind               string
	Replicas           int32
	HasLocalStorage    bool
	HasGPU             bool
	MaxUnavailable     int32
	PDBMinAvailable    int32
	PDBMaxUnavailable  int32
	PDBFound           bool
	PriorityClass      string
	MonthlyCost        float64
}

type ClusterInfo struct {
	Nodes         []NodeInfo
	TotalNodes    int
	TotalPods     int
	PVCs          []PVCInfo
	LoadBalancers []LBServiceInfo
	Workloads     []WorkloadInfo
}
