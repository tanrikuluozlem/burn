package analyzer

import (
	"fmt"

	"github.com/tanrikuluozlem/burn/internal/collector"
)

type SpotReadiness struct {
	Name             string  `json:"name"`
	Namespace        string  `json:"namespace"`
	Kind             string  `json:"kind"`
	Replicas         int32   `json:"replicas"`
	Status           string  `json:"status"`
	Reason           string  `json:"reason"`
	MonthlyCost      float64 `json:"monthly_cost"`
	Discount         float64 `json:"discount,omitempty"`
	InterruptionRate int     `json:"interruption_rate,omitempty"`
	PricingSource    string  `json:"pricing_source,omitempty"`
}

func CheckSpotReadiness(workloads []collector.WorkloadInfo) []SpotReadiness {
	var results []SpotReadiness

	for _, w := range workloads {
		r := SpotReadiness{
			Name:        w.Name,
			Namespace:   w.Namespace,
			Kind:        w.Kind,
			Replicas:    w.Replicas,
			MonthlyCost: w.MonthlyCost,
		}

		if w.PriorityClass == "system-cluster-critical" || w.PriorityClass == "system-node-critical" {
			r.Status = "not-ready"
			r.Reason = fmt.Sprintf("priority %s — cluster-critical workload", w.PriorityClass)
			results = append(results, r)
			continue
		}

		if w.Kind == "DaemonSet" {
			r.Status = "not-ready"
			r.Reason = "DaemonSet — runs on every node, not a spot candidate"
			results = append(results, r)
			continue
		}

		if w.Kind == "StatefulSet" {
			r.Status = "not-ready"
			r.Reason = "StatefulSet — risk of data loss on interruption"
			results = append(results, r)
			continue
		}

		if w.Replicas < 2 {
			r.Status = "not-ready"
			r.Reason = "single replica — no redundancy if node is reclaimed"
			results = append(results, r)
			continue
		}

		if w.HasLocalStorage {
			r.Status = "not-ready"
			r.Reason = "uses local storage (emptyDir/hostPath) — data lost on eviction"
			results = append(results, r)
			continue
		}

		if w.HasGPU {
			r.Status = "not-ready"
			r.Reason = "GPU workload — interruption risks training loss or inference downtime"
			results = append(results, r)
			continue
		}

		if w.PDBFound {
			minAvail := w.PDBMinAvailable
			if w.PDBMaxUnavailable > 0 {
				minAvail = w.Replicas - w.PDBMaxUnavailable
			}
			if minAvail > 0 {
				ratio := float64(minAvail) / float64(w.Replicas)
				if ratio > 0.5 {
					r.Status = "not-ready"
					r.Reason = fmt.Sprintf("PDB too strict — minAvailable/replicas = %.0f%% (>50%%)", ratio*100)
					results = append(results, r)
					continue
				}
			}
		}

		if w.Kind == "Deployment" && w.MaxUnavailable > 0 && w.Replicas >= 3 {
			available := float64(w.Replicas-w.MaxUnavailable) / float64(w.Replicas)
			if available < 0.9 {
				r.Status = "not-ready"
				r.Reason = fmt.Sprintf("rolling update too aggressive — only %.0f%% available during deploy", available*100)
				results = append(results, r)
				continue
			}
		}

		r.Status = "spot-ready"
		r.Reason = fmt.Sprintf("%d replicas, no local storage, %s", w.Replicas, w.Kind)
		results = append(results, r)
	}

	return results
}

func SpotSavings(results []SpotReadiness) float64 {
	var total float64
	for _, r := range results {
		if r.Status == "spot-ready" && r.Discount > 0 {
			total += r.MonthlyCost * r.Discount
		}
	}
	return total
}
