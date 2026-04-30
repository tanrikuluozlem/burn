package pricing

const (
	// Default pricing rates for CPU-to-RAM cost ratio derivation.
	DefaultCPUCostPerCoreHr = 0.031611
	DefaultRAMCostPerGiBHr  = 0.004237

	// CPUToRAMRatio — relative cost weight of 1 CPU core vs 1 GiB RAM.
	CPUToRAMRatio = DefaultCPUCostPerCoreHr / DefaultRAMCostPerGiBHr // ~7.46
)

// SplitNodeCost splits a node's hourly price into per-core CPU and per-GiB RAM rates.
// cpuAllocatable is in millicores, memAllocatable is in bytes.
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
