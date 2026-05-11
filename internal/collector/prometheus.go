package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const maxResponseSize = 10 * 1024 * 1024 // 10MB

type PrometheusClient struct {
	baseURL    string
	httpClient *http.Client
	period     string
}

func NewPrometheusClient(baseURL, period string) *PrometheusClient {
	return &PrometheusClient{
		baseURL: baseURL,
		period:  period,
		httpClient: &http.Client{
			Timeout: 2 * time.Minute,
		},
	}
}

func (p *PrometheusClient) wrapQuery(query string) string {
	if p.period == "" {
		return query
	}
	return fmt.Sprintf("avg_over_time(%s[%s:5m])", query, p.period)
}

type promResponse struct {
	Status string   `json:"status"`
	Data   promData `json:"data"`
}

type promData struct {
	ResultType string       `json:"resultType"`
	Result     []promResult `json:"result"`
}

type promResult struct {
	Metric map[string]string `json:"metric"`
	Value  []any             `json:"value"`
}

func (p *PrometheusClient) Query(ctx context.Context, query string) ([]promResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/api/v1/query", nil)
	if err != nil {
		return nil, err
	}

	q := url.Values{}
	q.Set("query", query)
	req.URL.RawQuery = q.Encode()

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("prometheus: %d", resp.StatusCode)
	}

	var result promResponse
	limitedReader := io.LimitReader(resp.Body, maxResponseSize)
	if err := json.NewDecoder(limitedReader).Decode(&result); err != nil {
		return nil, fmt.Errorf("prometheus decode error (response may exceed %dMB limit): %w", maxResponseSize/(1024*1024), err)
	}

	if result.Status != "success" {
		return nil, fmt.Errorf("prometheus: %s", result.Status)
	}
	return result.Data.Result, nil
}

func (p *PrometheusClient) GetNodeCPUUsage(ctx context.Context) (map[string]float64, error) {
	// Use instance label (standard node-exporter) - try node label first (kube-prometheus-stack)
	query := p.wrapQuery(`sum(rate(node_cpu_seconds_total{mode!="idle"}[5m])) by (instance)`)
	results, err := p.Query(ctx, query)
	if err != nil {
		return nil, err
	}

	usage := make(map[string]float64)
	for _, r := range results {
		node := r.Metric["node"]
		if node == "" {
			node = r.Metric["instance"]
		}
		if node == "" {
			continue
		}
		if val, err := parseValue(r.Value); err == nil {
			usage[node] = val
		}
	}
	return usage, nil
}

func (p *PrometheusClient) GetNodeMemoryUsage(ctx context.Context) (map[string]int64, error) {
	query := p.wrapQuery(`sum(node_memory_MemTotal_bytes - node_memory_MemAvailable_bytes) by (instance)`)
	results, err := p.Query(ctx, query)
	if err != nil {
		return nil, err
	}

	usage := make(map[string]int64)
	for _, r := range results {
		node := r.Metric["node"]
		if node == "" {
			node = r.Metric["instance"]
		}
		if node == "" {
			continue
		}
		if val, err := parseValue(r.Value); err == nil {
			usage[node] = int64(val)
		}
	}
	return usage, nil
}

func (p *PrometheusClient) GetPodCPUUsage(ctx context.Context) (map[string]float64, error) {
	query := p.wrapQuery(`sum(rate(container_cpu_usage_seconds_total{container!="",container!="POD"}[5m])) by (pod, namespace)`)
	results, err := p.Query(ctx, query)
	if err != nil {
		return nil, err
	}

	usage := make(map[string]float64)
	for _, r := range results {
		pod := r.Metric["pod"]
		ns := r.Metric["namespace"]
		if pod == "" || ns == "" {
			continue
		}
		key := ns + "/" + pod
		if val, err := parseValue(r.Value); err == nil {
			usage[key] = val
		}
	}
	return usage, nil
}

func (p *PrometheusClient) GetPodMemoryUsage(ctx context.Context) (map[string]int64, error) {
	query := p.wrapQuery(`sum(container_memory_working_set_bytes{container!="",container!="POD"}) by (pod, namespace)`)
	results, err := p.Query(ctx, query)
	if err != nil {
		return nil, err
	}

	usage := make(map[string]int64)
	for _, r := range results {
		pod := r.Metric["pod"]
		ns := r.Metric["namespace"]
		if pod == "" || ns == "" {
			continue
		}
		key := ns + "/" + pod
		if val, err := parseValue(r.Value); err == nil {
			usage[key] = int64(val)
		}
	}
	return usage, nil
}

func (p *PrometheusClient) wrapQuantileQuery(query string, quantile float64) string {
	if p.period == "" {
		return query
	}
	return fmt.Sprintf("quantile_over_time(%g, %s[%s:5m])", quantile, query, p.period)
}

func (p *PrometheusClient) GetPodCPUUsageP95(ctx context.Context) (map[string]float64, error) {
	baseQuery := `sum(rate(container_cpu_usage_seconds_total{container!="",container!="POD"}[5m])) by (pod, namespace)`
	query := p.wrapQuantileQuery(baseQuery, 0.95)
	results, err := p.Query(ctx, query)
	if err != nil {
		return nil, err
	}

	usage := make(map[string]float64)
	for _, r := range results {
		pod := r.Metric["pod"]
		ns := r.Metric["namespace"]
		if pod == "" || ns == "" {
			continue
		}
		key := ns + "/" + pod
		if val, err := parseValue(r.Value); err == nil {
			usage[key] = val
		}
	}
	return usage, nil
}

func (p *PrometheusClient) GetPodMemoryUsageP95(ctx context.Context) (map[string]int64, error) {
	baseQuery := `sum(container_memory_working_set_bytes{container!="",container!="POD"}) by (pod, namespace)`
	query := p.wrapQuantileQuery(baseQuery, 0.95)
	results, err := p.Query(ctx, query)
	if err != nil {
		return nil, err
	}

	usage := make(map[string]int64)
	for _, r := range results {
		pod := r.Metric["pod"]
		ns := r.Metric["namespace"]
		if pod == "" || ns == "" {
			continue
		}
		key := ns + "/" + pod
		if val, err := parseValue(r.Value); err == nil {
			usage[key] = int64(val)
		}
	}
	return usage, nil
}

func (p *PrometheusClient) GetNodeNetworkEgress(ctx context.Context) (map[string]float64, error) {
	query := p.wrapQuery(`sum(rate(node_network_transmit_bytes_total{device!="lo"}[5m])) by (instance)`)
	results, err := p.Query(ctx, query)
	if err != nil {
		return nil, err
	}

	usage := make(map[string]float64)
	for _, r := range results {
		node := r.Metric["node"]
		if node == "" {
			node = r.Metric["instance"]
		}
		if node == "" {
			continue
		}
		if val, err := parseValue(r.Value); err == nil {
			usage[node] = val // bytes per second
		}
	}
	return usage, nil
}

func parseValue(v []any) (float64, error) {
	if len(v) < 2 {
		return 0, fmt.Errorf("invalid value")
	}
	s, ok := v[1].(string)
	if !ok {
		return 0, fmt.Errorf("not a string")
	}
	return strconv.ParseFloat(s, 64)
}
