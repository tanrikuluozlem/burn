output "grafana_url" {
  description = "Grafana URL (NodePort)"
  value       = "http://<NODE_IP>:30090"
}

output "prometheus_url" {
  description = "Prometheus URL (ClusterIP)"
  value       = "http://prometheus-kube-prometheus-prometheus.monitoring:9090"
}
