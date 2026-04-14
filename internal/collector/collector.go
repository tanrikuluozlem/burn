package collector

import (
	"context"
	"fmt"
	"log"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

type Collector struct {
	client     kubernetes.Interface
	prometheus *PrometheusClient
}

func New(kubeconfig, prometheusURL string) (*Collector, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		loadingRules.ExplicitPath = kubeconfig
	}

	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		loadingRules,
		&clientcmd.ConfigOverrides{},
	).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load kubeconfig: %w", err)
	}

	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	c := &Collector{client: client}

	if prometheusURL != "" {
		c.prometheus = NewPrometheusClient(prometheusURL)
	}

	return c, nil
}

func (c *Collector) Collect(ctx context.Context) (*ClusterInfo, error) {
	nodes, err := c.client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	pods, err := c.client.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	// group pods by node
	podsByNode := make(map[string][]PodInfo)
	for _, pod := range pods.Items {
		if pod.Spec.NodeName == "" {
			continue
		}
		podsByNode[pod.Spec.NodeName] = append(podsByNode[pod.Spec.NodeName], parsePod(pod))
	}

	var nodeInfos []NodeInfo
	for _, node := range nodes.Items {
		nodeInfo := parseNode(node)
		nodeInfo.Pods = podsByNode[node.Name]
		nodeInfos = append(nodeInfos, nodeInfo)
	}

	// Enrich with Prometheus metrics if available
	if c.prometheus != nil {
		c.enrichWithMetrics(ctx, nodeInfos)
	}

	return &ClusterInfo{
		Nodes:      nodeInfos,
		TotalNodes: len(nodeInfos),
		TotalPods:  len(pods.Items),
	}, nil
}

func (c *Collector) enrichWithMetrics(ctx context.Context, nodes []NodeInfo) {
	// Fetch node metrics
	nodeCPU, err := c.prometheus.GetNodeCPUUsage(ctx)
	if err != nil {
		log.Printf("warning: failed to get node CPU metrics: %v", err)
	}
	nodeMem, err := c.prometheus.GetNodeMemoryUsage(ctx)
	if err != nil {
		log.Printf("warning: failed to get node memory metrics: %v", err)
	}

	// Fetch pod metrics
	podCPU, err := c.prometheus.GetPodCPUUsage(ctx)
	if err != nil {
		log.Printf("warning: failed to get pod CPU metrics: %v", err)
	}
	podMem, err := c.prometheus.GetPodMemoryUsage(ctx)
	if err != nil {
		log.Printf("warning: failed to get pod memory metrics: %v", err)
	}

	// remap by IP for matching (prometheus uses 10.0.1.5:9100, k8s uses ip-10-0-1-5.ec2.internal)
	cpuByIP := make(map[string]float64)
	memByIP := make(map[string]int64)
	for k, v := range nodeCPU {
		cpuByIP[extractIP(k)] = v
	}
	for k, v := range nodeMem {
		memByIP[extractIP(k)] = v
	}

	for i := range nodes {
		ip := extractIPFromNodeName(nodes[i].Name)
		if cpu, ok := cpuByIP[ip]; ok {
			nodes[i].CPUUsage = cpu
		}
		if mem, ok := memByIP[ip]; ok {
			nodes[i].MemoryUsage = mem
		}

		// Enrich pods on this node
		for j := range nodes[i].Pods {
			pod := &nodes[i].Pods[j]
			key := pod.Namespace + "/" + pod.Name
			if cpu, ok := podCPU[key]; ok {
				pod.CPUUsage = cpu
			}
			if mem, ok := podMem[key]; ok {
				pod.MemoryUsage = mem
			}
		}
	}
}

func parseNode(node corev1.Node) NodeInfo {
	labels := node.Labels
	cloud := detectCloudProvider(labels)

	return NodeInfo{
		Name:           node.Name,
		InstanceType:   labels["node.kubernetes.io/instance-type"],
		Region:         labels["topology.kubernetes.io/region"],
		Zone:           labels["topology.kubernetes.io/zone"],
		CloudProvider:  cloud,
		CPUCores:       node.Status.Capacity.Cpu().MilliValue() / 1000,
		MemoryBytes:    node.Status.Capacity.Memory().Value(),
		CPUAllocatable: node.Status.Allocatable.Cpu().MilliValue() / 1000,
		MemAllocatable: node.Status.Allocatable.Memory().Value(),
		IsSpot:         isSpotInstance(labels),
		Labels:         labels,
	}
}

func detectCloudProvider(labels map[string]string) CloudProvider {
	// AWS/EKS
	if _, ok := labels["eks.amazonaws.com/nodegroup"]; ok {
		return CloudAWS
	}
	// GCP/GKE
	if _, ok := labels["cloud.google.com/gke-nodepool"]; ok {
		return CloudGCP
	}
	// Azure/AKS
	if _, ok := labels["kubernetes.azure.com/cluster"]; ok {
		return CloudAzure
	}
	return CloudUnknown
}

func isSpotInstance(labels map[string]string) bool {
	// AWS/EKS
	if labels["eks.amazonaws.com/capacityType"] == "SPOT" {
		return true
	}
	// Karpenter (works across clouds)
	if labels["karpenter.sh/capacity-type"] == "spot" {
		return true
	}
	// GCP/GKE preemptible
	if labels["cloud.google.com/gke-preemptible"] == "true" {
		return true
	}
	// Azure/AKS spot
	if labels["kubernetes.azure.com/scalesetpriority"] == "spot" {
		return true
	}
	return false
}

func extractIP(s string) string {
	if idx := strings.LastIndex(s, ":"); idx != -1 {
		return s[:idx]
	}
	return s
}

func extractIPFromNodeName(name string) string {
	if strings.HasPrefix(name, "ip-") {
		parts := strings.SplitN(name, ".", 2)
		ip := strings.TrimPrefix(parts[0], "ip-")
		return strings.ReplaceAll(ip, "-", ".")
	}
	return name
}

func parsePod(pod corev1.Pod) PodInfo {
	var cpuReq, cpuLim, memReq, memLim int64

	for _, container := range pod.Spec.Containers {
		cpuReq += container.Resources.Requests.Cpu().MilliValue()
		cpuLim += container.Resources.Limits.Cpu().MilliValue()
		memReq += container.Resources.Requests.Memory().Value()
		memLim += container.Resources.Limits.Memory().Value()
	}

	return PodInfo{
		Name:          pod.Name,
		Namespace:     pod.Namespace,
		CPURequest:    cpuReq,
		CPULimit:      cpuLim,
		MemoryRequest: memReq,
		MemoryLimit:   memLim,
	}
}
