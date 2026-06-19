package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	maxResponseSize = 10 * 1024 * 1024 // 10MB
	maxRetries      = 3
	retryBaseDelay  = 500 * time.Millisecond
	retryMaxDelay   = 10 * time.Second
)

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
			Transport: &http.Transport{
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     120 * time.Second,
			},
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

func (p *PrometheusClient) retryableDo(req *http.Request) (*http.Response, error) {
	var resp *http.Response
	var err error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		resp, err = p.httpClient.Do(req)
		if err != nil {
			return nil, err
		}

		// Check for retryable status: 429, 5xx, or AWS AMP ThrottlingException (400)
		retryable := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		if !retryable && resp.StatusCode == http.StatusBadRequest {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if strings.Contains(string(body), "ThrottlingException") {
				retryable = true
			} else {
				// Not throttled, return a new reader with the body we already read
				resp.Body = io.NopCloser(strings.NewReader(string(body)))
				return resp, nil
			}
		}
		if !retryable {
			return resp, nil
		}

		if attempt == maxRetries {
			resp.Body.Close()
			return nil, fmt.Errorf("prometheus: max retries exceeded, last status %d", resp.StatusCode)
		}

		// Calculate delay
		delay := time.Duration(float64(retryBaseDelay) * math.Pow(2, float64(attempt)))
		if delay > retryMaxDelay {
			delay = retryMaxDelay
		}

		// Honor Retry-After header
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			if seconds, parseErr := strconv.Atoi(ra); parseErr == nil {
				delay = time.Duration(seconds) * time.Second
			}
		}

		resp.Body.Close()

		select {
		case <-req.Context().Done():
			return nil, req.Context().Err()
		case <-time.After(delay):
		}
	}

	return nil, fmt.Errorf("prometheus: request failed")
}

func (p *PrometheusClient) Query(ctx context.Context, query string) ([]promResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/api/v1/query", nil)
	if err != nil {
		return nil, err
	}

	q := url.Values{}
	q.Set("query", query)
	req.URL.RawQuery = q.Encode()

	resp, err := p.retryableDo(req)
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
	// Use instance label (standard node-exporter)
	query := p.wrapQuery(`sum(rate(node_cpu_seconds_total{mode!="idle"}[5m])) by (instance)`)
	results, err := p.Query(ctx, query)
	if err != nil {
		return nil, err
	}

	usage := make(map[string]float64)
	for _, r := range results {
		node := r.Metric["instance"]
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
		node := r.Metric["instance"]
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

// CheckDataRange queries prometheus_tsdb_lowest_timestamp_seconds to determine
// how far back data exists. Returns the number of available days.
// If the metric is unavailable (managed Prometheus, federation), returns -1.
func (p *PrometheusClient) CheckDataRange(ctx context.Context) float64 {
	results, err := p.Query(ctx, "prometheus_tsdb_lowest_timestamp_seconds")
	if err != nil || len(results) == 0 {
		return -1
	}
	val, err := parseValue(results[0].Value)
	if err != nil || val <= 0 {
		return -1
	}
	oldest := time.Unix(int64(val), 0)
	days := time.Since(oldest).Hours() / 24
	return days
}

// PeriodToDays converts a Prometheus duration string (e.g. "7d", "24h") to days.
// Returns -1 for unparseable values.
func PeriodToDays(period string) float64 {
	if period == "" {
		return -1
	}
	n := len(period)
	if n < 2 {
		return -1
	}
	val, err := strconv.ParseFloat(period[:n-1], 64)
	if err != nil {
		return -1
	}
	switch period[n-1] {
	case 's':
		return val / 86400
	case 'm':
		return val / 1440
	case 'h':
		return val / 24
	case 'd':
		return val
	case 'w':
		return val * 7
	case 'y':
		return val * 365
	default:
		return -1
	}
}

func parseValue(v []any) (float64, error) {
	if len(v) < 2 {
		return 0, fmt.Errorf("invalid value")
	}
	s, ok := v[1].(string)
	if !ok {
		return 0, fmt.Errorf("not a string")
	}
	val, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	if math.IsNaN(val) || math.IsInf(val, 0) {
		return 0, fmt.Errorf("invalid value: %s", s)
	}
	return val, nil
}
