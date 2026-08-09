package collector

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func TestResolveWorkloadOwner(t *testing.T) {
	rsIndex := map[string]rsOwner{
		"prod/web-abc123":    {Kind: "Deployment", Name: "web"},
		"prod/api-def456":    {Kind: "Deployment", Name: "api"},
		"staging/web-aaa111": {Kind: "Deployment", Name: "web"},
	}

	tests := []struct {
		name     string
		pod      corev1.Pod
		wantKind string
		wantName string
	}{
		{
			name: "Deployment via ReplicaSet",
			pod: corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "web-abc123-xyz", Namespace: "prod",
					OwnerReferences: []metav1.OwnerReference{
						{Kind: "ReplicaSet", Name: "web-abc123", Controller: ptr.To(true)},
					},
				},
			},
			wantKind: "Deployment", wantName: "web",
		},
		{
			name: "StatefulSet direct",
			pod: corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "db-0", Namespace: "prod",
					OwnerReferences: []metav1.OwnerReference{
						{Kind: "StatefulSet", Name: "db", Controller: ptr.To(true)},
					},
				},
			},
			wantKind: "StatefulSet", wantName: "db",
		},
		{
			name: "DaemonSet direct",
			pod: corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "agent-xyz", Namespace: "prod",
					OwnerReferences: []metav1.OwnerReference{
						{Kind: "DaemonSet", Name: "agent", Controller: ptr.To(true)},
					},
				},
			},
			wantKind: "DaemonSet", wantName: "agent",
		},
		{
			name: "ReplicaSet not owned by Deployment",
			pod: corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "orphan-rs-pod", Namespace: "prod",
					OwnerReferences: []metav1.OwnerReference{
						{Kind: "ReplicaSet", Name: "standalone-rs", Controller: ptr.To(true)},
					},
				},
			},
			wantKind: "", wantName: "",
		},
		{
			name: "controller is not first ownerReference",
			pod: corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "multi-owner", Namespace: "prod",
					OwnerReferences: []metav1.OwnerReference{
						{Kind: "ConfigMap", Name: "irrelevant", Controller: ptr.To(false)},
						{Kind: "ReplicaSet", Name: "api-def456", Controller: ptr.To(true)},
					},
				},
			},
			wantKind: "Deployment", wantName: "api",
		},
		{
			name: "same RS name different namespace",
			pod: corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "web-staging-pod", Namespace: "staging",
					OwnerReferences: []metav1.OwnerReference{
						{Kind: "ReplicaSet", Name: "web-aaa111", Controller: ptr.To(true)},
					},
				},
			},
			wantKind: "Deployment", wantName: "web",
		},
		{
			name: "RS in index from different namespace does not collide",
			pod: corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "web-other-pod", Namespace: "other",
					OwnerReferences: []metav1.OwnerReference{
						{Kind: "ReplicaSet", Name: "web-abc123", Controller: ptr.To(true)},
					},
				},
			},
			wantKind: "", wantName: "", // "other/web-abc123" not in index
		},
		{
			name: "no ownerReferences",
			pod: corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "standalone", Namespace: "prod"},
			},
			wantKind: "", wantName: "",
		},
		{
			name: "no controller ownerReference",
			pod: corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "no-controller", Namespace: "prod",
					OwnerReferences: []metav1.OwnerReference{
						{Kind: "ReplicaSet", Name: "web-abc123", Controller: ptr.To(false)},
					},
				},
			},
			wantKind: "", wantName: "",
		},
		{
			name: "Job owner not resolved",
			pod: corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "job-pod", Namespace: "prod",
					OwnerReferences: []metav1.OwnerReference{
						{Kind: "Job", Name: "batch-job", Controller: ptr.To(true)},
					},
				},
			},
			wantKind: "", wantName: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kind, name := resolveWorkloadOwner(&tt.pod, rsIndex)
			if kind != tt.wantKind || name != tt.wantName {
				t.Errorf("got (%q, %q), want (%q, %q)", kind, name, tt.wantKind, tt.wantName)
			}
		})
	}
}

func TestP95AvailabilityFromMapMembership(t *testing.T) {
	pods := []PodInfo{
		{Name: "has-p95", Namespace: "ns"},
		{Name: "missing", Namespace: "ns"},
		{Name: "zero-p95", Namespace: "ns"},
	}

	cpuP95 := map[string]float64{
		"ns/has-p95":  0.5,
		"ns/zero-p95": 0.0, // measured zero
	}
	memP95 := map[string]int64{
		"ns/has-p95": 500 << 20,
		// missing and zero-p95 absent
	}

	for i := range pods {
		key := pods[i].Namespace + "/" + pods[i].Name
		if cpu, ok := cpuP95[key]; ok {
			pods[i].CPUP95Usage = cpu
			pods[i].CPUP95Available = true
		}
		if mem, ok := memP95[key]; ok {
			pods[i].MemoryP95Usage = mem
			pods[i].MemP95Available = true
		}
	}

	// has-p95: both available
	if !pods[0].CPUP95Available || !pods[0].MemP95Available {
		t.Error("has-p95 should have both P95 available")
	}
	if pods[0].CPUP95Usage != 0.5 {
		t.Errorf("has-p95 CPU P95 = %v, want 0.5", pods[0].CPUP95Usage)
	}

	// missing: neither available
	if pods[1].CPUP95Available || pods[1].MemP95Available {
		t.Error("missing should have no P95 available")
	}

	// zero-p95: CPU available (measured zero), MEM unavailable
	if !pods[2].CPUP95Available {
		t.Error("zero-p95 should have CPU P95 available (measured zero)")
	}
	if pods[2].CPUP95Usage != 0 {
		t.Errorf("zero-p95 CPU P95 = %v, want 0", pods[2].CPUP95Usage)
	}
	if pods[2].MemP95Available {
		t.Error("zero-p95 should not have MEM P95 available")
	}
}
