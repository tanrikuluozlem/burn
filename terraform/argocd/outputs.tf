output "grafana_admin_password" {
  description = "Grafana admin password"
  value       = random_password.grafana_admin.result
  sensitive   = true
}

output "grafana_url" {
  description = "Grafana URL (NodePort)"
  value       = "http://<NODE_IP>:30080"
}

output "prometheus_url" {
  description = "Prometheus URL (ClusterIP)"
  value       = "http://prometheus-kube-prometheus-prometheus.monitoring:9090"
}
