package collector

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

const testHostname = "k8s-example-123.eu-central-1.elb.amazonaws.com"

// collectLBs exercises the real Collect() path with a fake K8s client.
func collectLBs(t *testing.T, objects ...metav1.Object) []LBServiceInfo {
	t.Helper()
	client := fake.NewSimpleClientset()
	ctx := context.Background()

	for _, obj := range objects {
		switch o := obj.(type) {
		case *corev1.Service:
			_, err := client.CoreV1().Services(o.Namespace).Create(ctx, o, metav1.CreateOptions{})
			if err != nil {
				t.Fatalf("create service %s/%s: %v", o.Namespace, o.Name, err)
			}
		case *networkingv1.Ingress:
			_, err := client.NetworkingV1().Ingresses(o.Namespace).Create(ctx, o, metav1.CreateOptions{})
			if err != nil {
				t.Fatalf("create ingress %s/%s: %v", o.Namespace, o.Name, err)
			}
		default:
			t.Fatalf("unsupported object type %T", obj)
		}
	}
	c := &Collector{client: client}
	info, err := c.Collect(ctx)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	return info.LoadBalancers
}

func makeIngress(name, namespace, hostname string) *networkingv1.Ingress {
	ing := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
	}
	if hostname != "" {
		ing.Status.LoadBalancer.Ingress = []networkingv1.IngressLoadBalancerIngress{
			{Hostname: hostname},
		}
	}
	return ing
}

func makeLBService(name, namespace, hostname string) *corev1.Service {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
	}
	if hostname != "" {
		svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{
			{Hostname: hostname},
		}
	}
	return svc
}

func TestLBDiscovery_IngressOnly(t *testing.T) {
	lbs := collectLBs(t,
		makeIngress("my-ing", "prod", testHostname),
	)

	if len(lbs) != 1 {
		t.Fatalf("expected 1 LB for single Ingress, got %d", len(lbs))
	}
	if lbs[0].Hostname != testHostname {
		t.Errorf("hostname = %q, want %q", lbs[0].Hostname, testHostname)
	}
}

func TestLBDiscovery_MultipleIngresses_SameHost(t *testing.T) {
	lbs := collectLBs(t,
		makeIngress("ing-1", "ns-a", testHostname),
		makeIngress("ing-2", "ns-a", testHostname),
		makeIngress("ing-3", "ns-b", testHostname),
		makeIngress("ing-4", "ns-b", testHostname),
		makeIngress("ing-5", "ns-c", testHostname),
		makeIngress("ing-6", "ns-c", testHostname),
	)

	if len(lbs) != 1 {
		t.Fatalf("expected 1 LB for 6 Ingresses sharing one hostname, got %d", len(lbs))
	}
	if lbs[0].Hostname != testHostname {
		t.Errorf("hostname = %q, want %q", lbs[0].Hostname, testHostname)
	}
}

func TestLBDiscovery_ServiceAndIngress_SameHost(t *testing.T) {
	lbs := collectLBs(t,
		makeLBService("my-svc", "prod", testHostname),
		makeIngress("my-ing", "prod", testHostname),
	)

	if len(lbs) != 1 {
		t.Fatalf("expected 1 LB for Service+Ingress sharing one hostname, got %d: %+v", len(lbs), lbs)
	}
	if lbs[0].Hostname != testHostname {
		t.Errorf("hostname = %q, want %q", lbs[0].Hostname, testHostname)
	}
}

func TestLBDiscovery_DifferentHosts(t *testing.T) {
	hostA := "k8s-example-a-123.eu-central-1.elb.amazonaws.com"
	hostB := "k8s-example-b-456.eu-central-1.elb.amazonaws.com"

	lbs := collectLBs(t,
		makeIngress("ing-a", "ns-a", hostA),
		makeIngress("ing-b", "ns-b", hostB),
	)

	if len(lbs) != 2 {
		t.Fatalf("expected 2 LBs for different hostnames, got %d", len(lbs))
	}

	hosts := map[string]bool{}
	for _, lb := range lbs {
		hosts[lb.Hostname] = true
	}
	if !hosts[hostA] {
		t.Errorf("missing hostname %q", hostA)
	}
	if !hosts[hostB] {
		t.Errorf("missing hostname %q", hostB)
	}
}

func TestLBDiscovery_EmptyIngressStatus(t *testing.T) {
	// Case 1: no status.loadBalancer.ingress at all
	lbs := collectLBs(t,
		makeIngress("empty-ing", "ns-a", ""),
	)
	if len(lbs) != 0 {
		t.Fatalf("expected 0 LBs for empty Ingress status, got %d: %+v", len(lbs), lbs)
	}

	// Case 2: status entry exists but hostname and IP are both empty
	ing := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "blank-ing", Namespace: "ns-a"},
	}
	ing.Status.LoadBalancer.Ingress = []networkingv1.IngressLoadBalancerIngress{
		{Hostname: "", IP: ""},
	}
	lbs = collectLBs(t, ing)
	if len(lbs) != 0 {
		t.Fatalf("expected 0 LBs for blank Ingress status entry, got %d: %+v", len(lbs), lbs)
	}
}

func TestLBDiscovery_ServiceOnly(t *testing.T) {
	lbs := collectLBs(t,
		makeLBService("api-lb", "prod", testHostname),
	)

	if len(lbs) != 1 {
		t.Fatalf("expected 1 LB for single Service, got %d", len(lbs))
	}
	if lbs[0].Hostname != testHostname {
		t.Errorf("hostname = %q, want %q", lbs[0].Hostname, testHostname)
	}
	if lbs[0].Name != "api-lb" {
		t.Errorf("name = %q, want %q", lbs[0].Name, "api-lb")
	}
	if lbs[0].Namespace != "prod" {
		t.Errorf("namespace = %q, want %q", lbs[0].Namespace, "prod")
	}
}

func TestLBDiscovery_MultipleServices_SameHost(t *testing.T) {
	lbs := collectLBs(t,
		makeLBService("svc-1", "ns-a", testHostname),
		makeLBService("svc-2", "ns-b", testHostname),
	)

	if len(lbs) != 1 {
		t.Fatalf("expected 1 LB for 2 Services sharing one hostname, got %d: %+v", len(lbs), lbs)
	}
	if lbs[0].Hostname != testHostname {
		t.Errorf("hostname = %q, want %q", lbs[0].Hostname, testHostname)
	}
}

func TestLBDiscovery_ServiceAndIngress_DifferentHosts(t *testing.T) {
	hostA := "k8s-svc-aaa.eu-central-1.elb.amazonaws.com"
	hostB := "k8s-ing-bbb.eu-central-1.elb.amazonaws.com"

	lbs := collectLBs(t,
		makeLBService("my-svc", "prod", hostA),
		makeIngress("my-ing", "prod", hostB),
	)

	if len(lbs) != 2 {
		t.Fatalf("expected 2 LBs for different hostnames, got %d: %+v", len(lbs), lbs)
	}
	hosts := map[string]bool{}
	for _, lb := range lbs {
		hosts[lb.Hostname] = true
	}
	if !hosts[hostA] || !hosts[hostB] {
		t.Errorf("expected both %q and %q, got %v", hostA, hostB, hosts)
	}
}

func TestLBDiscovery_IPFallback_CrossSource(t *testing.T) {
	ip := "10.0.0.1"
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "svc-ip", Namespace: "prod"},
		Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
		Status: corev1.ServiceStatus{
			LoadBalancer: corev1.LoadBalancerStatus{
				Ingress: []corev1.LoadBalancerIngress{{IP: ip}},
			},
		},
	}
	ing := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "ing-ip", Namespace: "prod"},
		Status: networkingv1.IngressStatus{
			LoadBalancer: networkingv1.IngressLoadBalancerStatus{
				Ingress: []networkingv1.IngressLoadBalancerIngress{{IP: ip}},
			},
		},
	}

	lbs := collectLBs(t, svc, ing)

	if len(lbs) != 1 {
		t.Fatalf("expected 1 LB for Service+Ingress sharing IP %q, got %d: %+v", ip, len(lbs), lbs)
	}
	if lbs[0].Hostname != ip {
		t.Errorf("hostname = %q, want %q", lbs[0].Hostname, ip)
	}
}
