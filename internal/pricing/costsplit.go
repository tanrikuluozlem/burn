package pricing

const (
	// Default pricing rates for cost ratio derivation (GCP us-central1).
	DefaultCPUCostPerCoreHr = 0.031611
	DefaultRAMCostPerGiBHr  = 0.004237
	DefaultGPUCostPerHr     = 0.95

	// Ratios — relative cost weight vs 1 GiB RAM.
	CPUToRAMRatio = DefaultCPUCostPerCoreHr / DefaultRAMCostPerGiBHr // ~7.46
	GPUToRAMRatio = DefaultGPUCostPerHr / DefaultRAMCostPerGiBHr     // ~224
)

// SplitNodeCost splits a non-GPU node's hourly price into per-core CPU and per-GiB RAM rates.
func SplitNodeCost(hourlyPrice float64, cpuAllocatable int64, memAllocatable int64) (cpuPerCore, ramPerGiB float64) {
	cpuCores := float64(cpuAllocatable) / 1000.0
	ramGiB := float64(memAllocatable) / (1024 * 1024 * 1024)

	ramMultiple := cpuCores*CPUToRAMRatio + ramGiB
	if ramMultiple == 0 {
		return 0, 0
	}

	ramPerGiB = hourlyPrice / ramMultiple
	cpuPerCore = ramPerGiB * CPUToRAMRatio
	return
}

// SplitNodeCostWithGPU splits a GPU node's hourly price into CPU, RAM, and GPU rates.
func SplitNodeCostWithGPU(hourlyPrice float64, cpuAllocatable int64, memAllocatable int64, gpuCount int64) (cpuPerCore, ramPerGiB, gpuPerUnit float64) {
	cpuCores := float64(cpuAllocatable) / 1000.0
	ramGiB := float64(memAllocatable) / (1024 * 1024 * 1024)
	gpus := float64(gpuCount)

	ramMultiple := gpus*GPUToRAMRatio + cpuCores*CPUToRAMRatio + ramGiB
	if ramMultiple == 0 {
		return 0, 0, 0
	}

	ramPerGiB = hourlyPrice / ramMultiple
	cpuPerCore = ramPerGiB * CPUToRAMRatio
	gpuPerUnit = ramPerGiB * GPUToRAMRatio
	return
}
