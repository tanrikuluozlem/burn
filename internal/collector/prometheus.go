package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type PrometheusClient struct {
	baseURL    string
	httpClient *http.Client
}

func NewPrometheusClient(baseURL string) *PrometheusClient {
	return &PrometheusClient{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
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
	endpoint := fmt.Sprintf("%s/api/v1/query", p.baseURL)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	q := url.Values{}
	q.Set("query", query)
	req.URL.RawQuery = q.Encode()

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute query: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var result promResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if result.Status != "success" {
		return nil, fmt.Errorf("query failed with status: %s", result.Status)
	}

	return result.Data.Result, nil
}

func (p *PrometheusClient) GetNodeCPUUsage(ctx context.Context) (map[string]float64, error) {
	query := `sum(rate(node_cpu_seconds_total{mode!="idle"}[5m])) by (node)`
	results, err := p.Query(ctx, query)
	if err != nil {
		return nil, err
	}

	usage := make(map[string]float64)
	for _, r := range results {
		node := r.Metric["node"]
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
	query := `node_memory_MemTotal_bytes - node_memory_MemAvailable_bytes`
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
	query := `sum(rate(container_cpu_usage_seconds_total{container!="",container!="POD"}[5m])) by (pod, namespace)`
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
	query := `sum(container_memory_working_set_bytes{container!="",container!="POD"}) by (pod, namespace)`
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

func parseValue(v []any) (float64, error) {
	if len(v) < 2 {
		return 0, fmt.Errorf("invalid value format")
	}
	str, ok := v[1].(string)
	if !ok {
		return 0, fmt.Errorf("value is not a string")
	}
	return strconv.ParseFloat(str, 64)
}
