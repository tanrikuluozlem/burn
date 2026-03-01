package collector

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

type Collector struct {
	client kubernetes.Interface
}

func New(kubeconfig string) (*Collector, error) {
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("failed to load kubeconfig from %s: %w", kubeconfig, err)
	}

	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	return &Collector{client: client}, nil
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

	return &ClusterInfo{
		Nodes:      nodeInfos,
		TotalNodes: len(nodeInfos),
		TotalPods:  len(pods.Items),
	}, nil
}

func parseNode(node corev1.Node) NodeInfo {
	labels := node.Labels

	return NodeInfo{
		Name:           node.Name,
		InstanceType:   labels["node.kubernetes.io/instance-type"],
		Region:         labels["topology.kubernetes.io/region"],
		Zone:           labels["topology.kubernetes.io/zone"],
		CPUCores:       node.Status.Capacity.Cpu().MilliValue() / 1000,
		MemoryBytes:    node.Status.Capacity.Memory().Value(),
		CPUAllocatable: node.Status.Allocatable.Cpu().MilliValue() / 1000,
		MemAllocatable: node.Status.Allocatable.Memory().Value(),
		IsSpot:         isSpotInstance(labels),
		Labels:         labels,
	}
}

func isSpotInstance(labels map[string]string) bool {
	if labels["eks.amazonaws.com/capacityType"] == "SPOT" {
		return true
	}
	if labels["karpenter.sh/capacity-type"] == "spot" {
		return true
	}
	return false
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
