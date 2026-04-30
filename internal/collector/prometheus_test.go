package collector

import "testing"

func TestWrapQuery(t *testing.T) {
	tests := []struct {
		name   string
		period string
		query  string
		want   string
	}{
		{
			name:   "no period returns original query",
			period: "",
			query:  `sum(rate(node_cpu_seconds_total{mode!="idle"}[5m])) by (instance)`,
			want:   `sum(rate(node_cpu_seconds_total{mode!="idle"}[5m])) by (instance)`,
		},
		{
			name:   "7d period wraps with avg_over_time",
			period: "7d",
			query:  `sum(rate(node_cpu_seconds_total{mode!="idle"}[5m])) by (instance)`,
			want:   `avg_over_time(sum(rate(node_cpu_seconds_total{mode!="idle"}[5m])) by (instance)[7d:5m])`,
		},
		{
			name:   "24h period",
			period: "24h",
			query:  `sum(node_memory_MemTotal_bytes - node_memory_MemAvailable_bytes) by (instance)`,
			want:   `avg_over_time(sum(node_memory_MemTotal_bytes - node_memory_MemAvailable_bytes) by (instance)[24h:5m])`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &PrometheusClient{period: tt.period}
			got := p.wrapQuery(tt.query)
			if got != tt.want {
				t.Errorf("wrapQuery() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWrapQuantileQuery(t *testing.T) {
	tests := []struct {
		name     string
		period   string
		query    string
		quantile float64
		want     string
	}{
		{
			name:     "no period returns original query",
			period:   "",
			query:    `sum(rate(container_cpu_usage_seconds_total[5m])) by (pod, namespace)`,
			quantile: 0.95,
			want:     `sum(rate(container_cpu_usage_seconds_total[5m])) by (pod, namespace)`,
		},
		{
			name:     "7d period wraps with quantile_over_time p95",
			period:   "7d",
			query:    `sum(rate(container_cpu_usage_seconds_total[5m])) by (pod, namespace)`,
			quantile: 0.95,
			want:     `quantile_over_time(0.95, sum(rate(container_cpu_usage_seconds_total[5m])) by (pod, namespace)[7d:5m])`,
		},
		{
			name:     "24h period wraps with quantile_over_time p99",
			period:   "24h",
			query:    `sum(container_memory_working_set_bytes) by (pod, namespace)`,
			quantile: 0.99,
			want:     `quantile_over_time(0.99, sum(container_memory_working_set_bytes) by (pod, namespace)[24h:5m])`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &PrometheusClient{period: tt.period}
			got := p.wrapQuantileQuery(tt.query, tt.quantile)
			if got != tt.want {
				t.Errorf("wrapQuantileQuery() = %q, want %q", got, tt.want)
			}
		})
	}
}
