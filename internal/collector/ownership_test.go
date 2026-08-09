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
