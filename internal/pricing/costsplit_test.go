package pricing

import (
	"math"
	"testing"
)

func TestSplitNodeCost(t *testing.T) {
	const tolerance = 0.001

	tests := []struct {
		name           string
		hourlyPrice    float64
		cpuMillicores  int64
		memBytes       int64
		wantTotalCheck bool // verify cpuPerCore*cores + ramPerGiB*gib ≈ hourlyPrice
	}{
		{
			name:           "m5.xlarge (general purpose: 4 vCPU, 16 GiB)",
			hourlyPrice:    0.192,
			cpuMillicores:  4000,
			memBytes:       16 * 1024 * 1024 * 1024,
			wantTotalCheck: true,
		},
		{
			name:           "c5.xlarge (compute optimized: 4 vCPU, 8 GiB)",
			hourlyPrice:    0.17,
			cpuMillicores:  4000,
			memBytes:       8 * 1024 * 1024 * 1024,
			wantTotalCheck: true,
		},
		{
			name:           "r5.xlarge (memory optimized: 4 vCPU, 32 GiB)",
			hourlyPrice:    0.252,
			cpuMillicores:  4000,
			memBytes:       32 * 1024 * 1024 * 1024,
			wantTotalCheck: true,
		},
		{
			name:          "zero CPU",
			hourlyPrice:   0.10,
			cpuMillicores: 0,
			memBytes:      8 * 1024 * 1024 * 1024,
		},
		{
			name:          "zero RAM",
			hourlyPrice:   0.10,
			cpuMillicores: 4000,
			memBytes:      0,
		},
		{
			name:          "zero both",
			hourlyPrice:   0.10,
			cpuMillicores: 0,
			memBytes:      0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cpuPerCore, ramPerGiB := SplitNodeCost(tt.hourlyPrice, tt.cpuMillicores, tt.memBytes)

			if tt.cpuMillicores == 0 && tt.memBytes == 0 {
				if cpuPerCore != 0 || ramPerGiB != 0 {
					t.Errorf("expected 0,0 for zero resources, got cpu=%f ram=%f", cpuPerCore, ramPerGiB)
				}
				return
			}

			if cpuPerCore < 0 || ramPerGiB < 0 {
				t.Errorf("negative prices: cpu=%f ram=%f", cpuPerCore, ramPerGiB)
			}

			if tt.wantTotalCheck {
				cores := float64(tt.cpuMillicores) / 1000.0
				gib := float64(tt.memBytes) / (1024 * 1024 * 1024)
				total := cpuPerCore*cores + ramPerGiB*gib

				if math.Abs(total-tt.hourlyPrice) > tolerance {
					t.Errorf("total mismatch: cpu(%f)*%f + ram(%f)*%f = %f, want %f",
						cpuPerCore, cores, ramPerGiB, gib, total, tt.hourlyPrice)
				}

				// Verify ratio is maintained
				if ramPerGiB > 0 {
					actualRatio := cpuPerCore / ramPerGiB
					if math.Abs(actualRatio-CPUToRAMRatio) > 0.01 {
						t.Errorf("ratio mismatch: got %f, want %f", actualRatio, CPUToRAMRatio)
					}
				}
			}
		})
	}
}

func TestSplitNodeCostComputeVsMemory(t *testing.T) {
	// Compute-optimized should allocate more $ to CPU per core
	cpuC5, _ := SplitNodeCost(0.17, 4000, 8*1024*1024*1024)
	cpuR5, _ := SplitNodeCost(0.252, 4000, 32*1024*1024*1024)

	// c5 has higher CPU cost per core because less RAM dilutes the CPU weight less
	if cpuC5 <= cpuR5 {
		t.Errorf("compute-optimized should have higher cpuPerCore: c5=%f, r5=%f", cpuC5, cpuR5)
	}
}
