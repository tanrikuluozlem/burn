package collector

import (
	"context"
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"

	"golang.org/x/sync/errgroup"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

type Collector struct {
	client     kubernetes.Interface
	prometheus *PrometheusClient
	namespace  string
}

func New(kubeconfig, kubecontext, namespace, prometheusURL, period string) (*Collector, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		loadingRules.ExplicitPath = kubeconfig
	}

	overrides := &clientcmd.ConfigOverrides{}
	if kubecontext != "" {
		overrides.CurrentContext = kubecontext
	}

	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		loadingRules,
		overrides,
	).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load kubeconfig: %w", err)
	}

	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	c := &Collector{
		client:    client,
		namespace: namespace,
	}

	if prometheusURL != "" {
		c.prometheus = NewPrometheusClient(prometheusURL, period)
	}

	return c, nil
}

const pageSize int64 = 500

func (c *Collector) listAllNodes(ctx context.Context) ([]corev1.Node, error) {
	var all []corev1.Node
	opts := metav1.ListOptions{Limit: pageSize}
	for {
		list, err := c.client.CoreV1().Nodes().List(ctx, opts)
		if err != nil {
			return nil, err
		}
		all = append(all, list.Items...)
		if list.Continue == "" {
			break
		}
		opts.Continue = list.Continue
	}
	return all, nil
}

func (c *Collector) listAllPods(ctx context.Context) ([]corev1.Pod, error) {
	var all []corev1.Pod
	opts := metav1.ListOptions{Limit: pageSize}
	for {
		list, err := c.client.CoreV1().Pods(c.namespace).List(ctx, opts)
		if err != nil {
			return nil, err
		}
		all = append(all, list.Items...)
		if list.Continue == "" {
			break
		}
		opts.Continue = list.Continue
	}
	return all, nil
}

// extractCloudDiskID returns the cloud provider volume ID from a PersistentVolume.
// AWS CSI: vol-xxx, Azure CSI: /subscriptions/.../disks/disk-name
// Falls back to legacy spec fields (awsElasticBlockStore, azureDisk).
func extractCloudDiskID(pv *corev1.PersistentVolume) string {
	if pv.Spec.CSI != nil && pv.Spec.CSI.VolumeHandle != "" {
		return pv.Spec.CSI.VolumeHandle
	}
	if pv.Spec.AWSElasticBlockStore != nil {
		return pv.Spec.AWSElasticBlockStore.VolumeID
	}
	if pv.Spec.AzureDisk != nil {
		return pv.Spec.AzureDisk.DiskName
	}
	if pv.Spec.GCEPersistentDisk != nil {
		return pv.Spec.GCEPersistentDisk.PDName
	}
	return ""
}

func (c *Collector) listAllPVCs(ctx context.Context) ([]corev1.PersistentVolumeClaim, error) {
	var all []corev1.PersistentVolumeClaim
	opts := metav1.ListOptions{Limit: pageSize}
	for {
		list, err := c.client.CoreV1().PersistentVolumeClaims(c.namespace).List(ctx, opts)
		if err != nil {
			return nil, err
		}
		all = append(all, list.Items...)
		if list.Continue == "" {
			break
		}
		opts.Continue = list.Continue
	}
	return all, nil
}

func (c *Collector) listAllServices(ctx context.Context) ([]corev1.Service, error) {
	var all []corev1.Service
	opts := metav1.ListOptions{Limit: pageSize}
	for {
		list, err := c.client.CoreV1().Services(c.namespace).List(ctx, opts)
		if err != nil {
			return nil, err
		}
		all = append(all, list.Items...)
		if list.Continue == "" {
			break
		}
		opts.Continue = list.Continue
	}
	return all, nil
}

func (c *Collector) listAllIngresses(ctx context.Context) ([]networkingv1.Ingress, error) {
	var all []networkingv1.Ingress
	opts := metav1.ListOptions{Limit: pageSize}
	for {
		list, err := c.client.NetworkingV1().Ingresses(c.namespace).List(ctx, opts)
		if err != nil {
			return nil, err
		}
		all = append(all, list.Items...)
		if list.Continue == "" {
			break
		}
		opts.Continue = list.Continue
	}
	return all, nil
}

func (c *Collector) Collect(ctx context.Context) (*ClusterInfo, error) {
	nodeItems, err := c.listAllNodes(ctx)
	if err != nil {
		return nil, err
	}

	podItems, err := c.listAllPods(ctx)
	if err != nil {
		return nil, err
	}

	if len(nodeItems) > 200 || len(podItems) > 5000 {
		log.Printf("warning: large cluster detected (%d nodes, %d pods) — analysis may take longer", len(nodeItems), len(podItems))
	}

	// group pods by node
	podsByNode := make(map[string][]PodInfo)
	for _, pod := range podItems {
		if pod.Spec.NodeName == "" {
			continue
		}
		podsByNode[pod.Spec.NodeName] = append(podsByNode[pod.Spec.NodeName], parsePod(pod))
	}

	var nodeInfos []NodeInfo
	for _, node := range nodeItems {
		nodeInfo := parseNode(node)
		nodeInfo.Pods = podsByNode[node.Name]
		nodeInfos = append(nodeInfos, nodeInfo)
	}

	// Enrich with Prometheus metrics if available
	if c.prometheus != nil {
		if c.prometheus.period != "" {
			c.validatePeriod(ctx)
		}
		c.enrichWithMetrics(ctx, nodeInfos)
	}

	// Collect PVCs and resolve cloud disk IDs from PersistentVolumes
	pvcs, err := c.listAllPVCs(ctx)
	if err != nil {
		log.Printf("warning: failed to list PVCs: %v", err)
	}

	// Build PV name → cloud disk ID map
	pvDiskIDs := make(map[string]string)
	pvList, pvErr := c.client.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
	if pvErr != nil {
		log.Printf("warning: failed to list PVs: %v", pvErr)
	} else {
		for _, pv := range pvList.Items {
			diskID := extractCloudDiskID(&pv)
			if diskID != "" {
				pvDiskIDs[pv.Name] = diskID
			}
		}
	}

	var pvcInfos []PVCInfo
	for _, pvc := range pvcs {
		if pvc.Status.Phase != corev1.ClaimBound {
			continue
		}
		var reqBytes int64
		if storage, ok := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; ok {
			reqBytes = storage.Value()
		}
		sc := ""
		if pvc.Spec.StorageClassName != nil {
			sc = *pvc.Spec.StorageClassName
		}
		pvcInfos = append(pvcInfos, PVCInfo{
			Name:           pvc.Name,
			Namespace:      pvc.Namespace,
			StorageClass:   sc,
			RequestedBytes: reqBytes,
			VolumeName:     pvc.Spec.VolumeName,
			CloudDiskID:    pvDiskIDs[pvc.Spec.VolumeName],
		})
	}

	// Collect LoadBalancer services
	services, err := c.listAllServices(ctx)
	if err != nil {
		log.Printf("warning: failed to list services: %v", err)
	}
	var lbInfos []LBServiceInfo
	for _, svc := range services {
		if svc.Spec.Type == corev1.ServiceTypeLoadBalancer {
			hostname := ""
			if len(svc.Status.LoadBalancer.Ingress) > 0 {
				hostname = svc.Status.LoadBalancer.Ingress[0].Hostname
				if hostname == "" {
					hostname = svc.Status.LoadBalancer.Ingress[0].IP
				}
			}
			lbInfos = append(lbInfos, LBServiceInfo{
				Name:      svc.Name,
				Namespace: svc.Namespace,
				Hostname:  hostname,
			})
		}
	}

	// Collect Ingress-based load balancers (ALB/NLB via Ingress controller)
	ingresses, err := c.listAllIngresses(ctx)
	if err != nil {
		log.Printf("warning: failed to list ingresses: %v", err)
	}
	seenLBs := make(map[string]bool)
	for _, ing := range ingresses {
		for _, lb := range ing.Status.LoadBalancer.Ingress {
			host := lb.Hostname
			if host == "" {
				host = lb.IP
			}
			if host == "" || seenLBs[host] {
				continue
			}
			seenLBs[host] = true
			lbInfos = append(lbInfos, LBServiceInfo{
				Name:      ing.Name,
				Namespace: ing.Namespace,
			})
		}
	}

	// Collect workloads (Deployments + StatefulSets) for spot-readiness
	workloads, err := c.collectWorkloads(ctx)
	if err != nil {
		log.Printf("warning: failed to collect workloads: %v", err)
	}

	return &ClusterInfo{
		Nodes:         nodeInfos,
		TotalNodes:    len(nodeInfos),
		TotalPods:     len(podItems),
		PVCs:          pvcInfos,
		LoadBalancers: lbInfos,
		Workloads:     workloads,
	}, nil
}

// Period returns the effective analysis period (may be adjusted if Prometheus data is limited).
func (c *Collector) Period() string {
	if c.prometheus != nil {
		return c.prometheus.period
	}
	return ""
}

func (c *Collector) validatePeriod(ctx context.Context) {
	requestedDays := PeriodToDays(c.prometheus.period)
	if requestedDays <= 0 {
		return
	}

	availableDays := c.prometheus.CheckDataRange(ctx)
	if availableDays < 0 {
		return // metric unavailable (managed Prometheus, federation) — skip check
	}

	if requestedDays > availableDays {
		adjustedDays := int(availableDays)
		if adjustedDays < 1 {
			adjustedDays = 1
		}
		adjusted := fmt.Sprintf("%dd", adjustedDays)
		log.Printf("warning: requested --period %s but Prometheus has only %dd of data — using %s",
			c.prometheus.period, adjustedDays, adjusted)
		c.prometheus.period = adjusted
	}
}

func (c *Collector) enrichWithMetrics(ctx context.Context, nodes []NodeInfo) {
	// Each query writes to its own variable — no shared writes, no mutex needed
	var (
		nodeCPU   map[string]float64
		nodeMem   map[string]int64
		podCPU    map[string]float64
		podMem    map[string]int64
		podCPUP95 map[string]float64
		podMemP95 map[string]int64
	)

	g, gctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		var err error
		nodeCPU, err = c.prometheus.GetNodeCPUUsage(gctx)
		if err != nil {
			log.Printf("warning: failed to get node CPU metrics: %v", err)
		}
		return nil
	})

	g.Go(func() error {
		var err error
		nodeMem, err = c.prometheus.GetNodeMemoryUsage(gctx)
		if err != nil {
			log.Printf("warning: failed to get node memory metrics: %v", err)
		}
		return nil
	})

	g.Go(func() error {
		var err error
		podCPU, err = c.prometheus.GetPodCPUUsage(gctx)
		if err != nil {
			log.Printf("warning: failed to get pod CPU metrics: %v", err)
		}
		return nil
	})

	g.Go(func() error {
		var err error
		podMem, err = c.prometheus.GetPodMemoryUsage(gctx)
		if err != nil {
			log.Printf("warning: failed to get pod memory metrics: %v", err)
		}
		return nil
	})

	if c.prometheus.period != "" {
		g.Go(func() error {
			var err error
			podCPUP95, err = c.prometheus.GetPodCPUUsageP95(gctx)
			if err != nil {
				log.Printf("warning: failed to get pod CPU p95 metrics: %v", err)
			}
			return nil
		})

		g.Go(func() error {
			var err error
			podMemP95, err = c.prometheus.GetPodMemoryUsageP95(gctx)
			if err != nil {
				log.Printf("warning: failed to get pod memory p95 metrics: %v", err)
			}
			return nil
		})
	}

	_ = g.Wait()

	// Log metric counts for debugging
	if len(nodeCPU) == 0 && len(podCPU) == 0 {
		log.Printf("warning: no Prometheus metrics received — cost report will use resource requests only")
	}

	// Merge results (single-threaded after Wait — no races)
	cpuByIP := make(map[string]float64)
	memByIP := make(map[string]int64)
	for k, v := range nodeCPU {
		cpuByIP[extractIP(k)] = v
	}
	for k, v := range nodeMem {
		memByIP[extractIP(k)] = v
	}

	for i := range nodes {
		ip := nodes[i].InternalIP
		if ip == "" {
			ip = extractIPFromNodeName(nodes[i].Name)
		}
		if cpu, ok := cpuByIP[ip]; ok {
			nodes[i].CPUUsage = cpu
		}
		if mem, ok := memByIP[ip]; ok {
			nodes[i].MemoryUsage = mem
		}

		for j := range nodes[i].Pods {
			pod := &nodes[i].Pods[j]
			key := pod.Namespace + "/" + pod.Name
			if cpu, ok := podCPU[key]; ok {
				pod.CPUUsage = cpu
			}
			if mem, ok := podMem[key]; ok {
				pod.MemoryUsage = mem
			}
			if cpu, ok := podCPUP95[key]; ok {
				pod.CPUP95Usage = cpu
			}
			if mem, ok := podMemP95[key]; ok {
				pod.MemoryP95Usage = mem
			}
		}
	}
}

func parseNode(node corev1.Node) NodeInfo {
	labels := node.Labels
	cloud := detectCloudProvider(labels)

	// GPU detection
	var gpuCount int64
	if gpu, ok := node.Status.Capacity["nvidia.com/gpu"]; ok {
		gpuCount = gpu.Value()
	}
	gpuType := labels["nvidia.com/gpu.product"]
	if gpuType == "" {
		gpuType = labels["cloud.google.com/gke-accelerator"]
	}

	return NodeInfo{
		Name:           node.Name,
		InternalIP:     getNodeInternalIP(node),
		InstanceType:   labels["node.kubernetes.io/instance-type"],
		Region:         labels["topology.kubernetes.io/region"],
		Zone:           labels["topology.kubernetes.io/zone"],
		CloudProvider:  cloud,
		CPUCores:       node.Status.Capacity.Cpu().MilliValue(),
		MemoryBytes:    node.Status.Capacity.Memory().Value(),
		CPUAllocatable: node.Status.Allocatable.Cpu().MilliValue(),
		MemAllocatable: node.Status.Allocatable.Memory().Value(),
		GPUCount:       gpuCount,
		GPUType:        gpuType,
		IsSpot:         isSpotInstance(labels),
		CreatedAt:      node.CreationTimestamp.Time,
		ProviderID:     node.Spec.ProviderID,
		Labels:         labels,
	}
}

func getNodeInternalIP(node corev1.Node) string {
	for _, addr := range node.Status.Addresses {
		if addr.Type == corev1.NodeInternalIP {
			return addr.Address
		}
	}
	return ""
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
	// Karpenter — detect cloud from instance type prefix or provider-specific labels
	if _, ok := labels["karpenter.sh/nodepool"]; ok {
		if _, ok := labels["karpenter.k8s.aws/instance-family"]; ok {
			return CloudAWS
		}
		if _, ok := labels["karpenter.azure.com/sku-family"]; ok {
			return CloudAzure
		}
		// Karpenter on AWS is most common, default to AWS
		return CloudAWS
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
	// GCP/GKE preemptible or spot
	if labels["cloud.google.com/gke-preemptible"] == "true" ||
		labels["cloud.google.com/gke-spot"] == "true" {
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
	var cpuReq, cpuLim, memReq, memLim, gpuReq int64

	for _, container := range pod.Spec.Containers {
		cpuReq += container.Resources.Requests.Cpu().MilliValue()
		cpuLim += container.Resources.Limits.Cpu().MilliValue()
		memReq += container.Resources.Requests.Memory().Value()
		memLim += container.Resources.Limits.Memory().Value()
		if gpu, ok := container.Resources.Requests["nvidia.com/gpu"]; ok {
			gpuReq += gpu.Value()
		}
	}

	return PodInfo{
		Name:          pod.Name,
		Namespace:     pod.Namespace,
		CPURequest:    cpuReq,
		CPULimit:      cpuLim,
		MemoryRequest: memReq,
		MemoryLimit:   memLim,
		GPURequest:    gpuReq,
	}
}

func (c *Collector) collectWorkloads(ctx context.Context) ([]WorkloadInfo, error) {
	var workloads []WorkloadInfo

	// List PDBs first — we'll match them to workloads by selector
	var pdbs []policyv1.PodDisruptionBudget
	opts := metav1.ListOptions{Limit: pageSize}
	for {
		list, err := c.client.PolicyV1().PodDisruptionBudgets(c.namespace).List(ctx, opts)
		if err != nil {
			log.Printf("warning: failed to list PDBs: %v", err)
			break
		}
		pdbs = append(pdbs, list.Items...)
		if list.Continue == "" {
			break
		}
		opts.Continue = list.Continue
	}

	// Deployments
	opts = metav1.ListOptions{Limit: pageSize}
	for {
		list, err := c.client.AppsV1().Deployments(c.namespace).List(ctx, opts)
		if err != nil {
			return nil, err
		}
		for _, d := range list.Items {
			w := WorkloadInfo{
				Name:      d.Name,
				Namespace: d.Namespace,
				Kind:      "Deployment",
				Replicas:  1,
			}
			if d.Spec.Replicas != nil {
				w.Replicas = *d.Spec.Replicas
			}

			// local storage check
			w.HasLocalStorage = hasLocalStorage(d.Spec.Template.Spec.Volumes)

			// priority class
			w.PriorityClass = d.Spec.Template.Spec.PriorityClassName

			w.HasGPU = hasGPURequest(d.Spec.Template.Spec.Containers)

			if d.Spec.Strategy.Type == appsv1.RollingUpdateDeploymentStrategyType &&
				d.Spec.Strategy.RollingUpdate != nil &&
				d.Spec.Strategy.RollingUpdate.MaxUnavailable != nil {
				w.MaxUnavailable = resolveIntOrPercent(d.Spec.Strategy.RollingUpdate.MaxUnavailable, w.Replicas)
			}

			w.PDBMinAvailable, w.PDBMaxUnavailable, w.PDBFound = matchPDB(pdbs, d.Spec.Selector.MatchLabels, w.Replicas)

			workloads = append(workloads, w)
		}
		if list.Continue == "" {
			break
		}
		opts.Continue = list.Continue
	}

	// StatefulSets
	opts = metav1.ListOptions{Limit: pageSize}
	for {
		list, err := c.client.AppsV1().StatefulSets(c.namespace).List(ctx, opts)
		if err != nil {
			return nil, err
		}
		for _, s := range list.Items {
			w := WorkloadInfo{
				Name:      s.Name,
				Namespace: s.Namespace,
				Kind:      "StatefulSet",
				Replicas:  1,
			}
			if s.Spec.Replicas != nil {
				w.Replicas = *s.Spec.Replicas
			}
			w.HasLocalStorage = hasLocalStorage(s.Spec.Template.Spec.Volumes) || len(s.Spec.VolumeClaimTemplates) > 0
			w.HasGPU = hasGPURequest(s.Spec.Template.Spec.Containers)
			w.PriorityClass = s.Spec.Template.Spec.PriorityClassName
			w.PDBMinAvailable, w.PDBMaxUnavailable, w.PDBFound = matchPDB(pdbs, s.Spec.Selector.MatchLabels, w.Replicas)
			workloads = append(workloads, w)
		}
		if list.Continue == "" {
			break
		}
		opts.Continue = list.Continue
	}

	// DaemonSets
	opts = metav1.ListOptions{Limit: pageSize}
	for {
		list, err := c.client.AppsV1().DaemonSets(c.namespace).List(ctx, opts)
		if err != nil {
			return nil, err
		}
		for _, ds := range list.Items {
			w := WorkloadInfo{
				Name:      ds.Name,
				Namespace: ds.Namespace,
				Kind:      "DaemonSet",
				Replicas:  ds.Status.DesiredNumberScheduled,
			}
			w.HasLocalStorage = hasLocalStorage(ds.Spec.Template.Spec.Volumes)
			w.HasGPU = hasGPURequest(ds.Spec.Template.Spec.Containers)
			w.PriorityClass = ds.Spec.Template.Spec.PriorityClassName
			if ds.Spec.Selector != nil {
				w.PDBMinAvailable, w.PDBMaxUnavailable, w.PDBFound = matchPDB(pdbs, ds.Spec.Selector.MatchLabels, w.Replicas)
			}
			workloads = append(workloads, w)
		}
		if list.Continue == "" {
			break
		}
		opts.Continue = list.Continue
	}

	return workloads, nil
}

func hasGPURequest(containers []corev1.Container) bool {
	for _, c := range containers {
		for name, q := range c.Resources.Requests {
			if isGPUResource(string(name)) && !q.IsZero() {
				return true
			}
		}
		for name, q := range c.Resources.Limits {
			if isGPUResource(string(name)) && !q.IsZero() {
				return true
			}
		}
	}
	return false
}

func isGPUResource(name string) bool {
	return strings.HasPrefix(name, "nvidia.com/") ||
		strings.HasPrefix(name, "amd.com/gpu") ||
		strings.HasPrefix(name, "intel.com/gpu")
}

func hasLocalStorage(volumes []corev1.Volume) bool {
	for _, v := range volumes {
		if v.HostPath != nil {
			return true
		}
		if v.EmptyDir != nil && v.EmptyDir.Medium != corev1.StorageMediumMemory {
			return true
		}
	}
	return false
}

func matchPDB(pdbs []policyv1.PodDisruptionBudget, workloadLabels map[string]string, replicas int32) (minAvail int32, maxUnavail int32, found bool) {
	for _, pdb := range pdbs {
		if pdb.Spec.Selector == nil {
			continue
		}
		selector, err := metav1.LabelSelectorAsSelector(pdb.Spec.Selector)
		if err != nil {
			continue
		}
		if selector.Matches(labels.Set(workloadLabels)) {
			if pdb.Spec.MinAvailable != nil {
				return resolveIntOrPercent(pdb.Spec.MinAvailable, replicas), 0, true
			}
			if pdb.Spec.MaxUnavailable != nil {
				return 0, resolveIntOrPercent(pdb.Spec.MaxUnavailable, replicas), true
			}
		}
	}
	return 0, 0, false
}

func resolveIntOrPercent(val *intstr.IntOrString, total int32) int32 {
	if val.Type == intstr.Int {
		return int32(val.IntValue())
	}
	pctStr := strings.TrimSuffix(val.StrVal, "%")
	pct, err := strconv.Atoi(pctStr)
	if err != nil || total <= 0 {
		return 0
	}
	return int32(math.Ceil(float64(pct) * float64(total) / 100.0))
}
