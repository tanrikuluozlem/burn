resource "kubectl_manifest" "monitoring_namespace" {
  yaml_body = yamlencode({
    apiVersion = "v1"
    kind       = "Namespace"
    metadata = {
      name = "monitoring"
    }
  })
}

resource "random_password" "grafana_admin" {
  length           = 24
  special          = true
  override_special = "!#%&*"
}

resource "kubectl_manifest" "grafana_admin_secret" {
  yaml_body = yamlencode({
    apiVersion = "v1"
    kind       = "Secret"
    metadata = {
      name      = "grafana-admin"
      namespace = "monitoring"
    }
    type = "Opaque"
    stringData = {
      admin-user     = "admin"
      admin-password = random_password.grafana_admin.result
    }
  })

  depends_on = [kubectl_manifest.monitoring_namespace]
}

resource "helm_release" "prometheus_stack" {
  namespace  = "monitoring"
  name       = "prometheus"
  repository = "https://prometheus-community.github.io/helm-charts"
  chart      = "kube-prometheus-stack"
  version    = "82.16.0"
  wait       = true
  timeout    = 900

  values = [
    <<-EOT
    prometheus:
      prometheusSpec:
        retention: 7d
        resources:
          requests:
            memory: 512Mi
            cpu: 250m
          limits:
            memory: 1Gi
        nodeSelector:
          eks.amazonaws.com/nodegroup: system
        serviceMonitorSelectorNilUsesHelmValues: false
        podMonitorSelectorNilUsesHelmValues: false

    grafana:
      enabled: true
      admin:
        existingSecret: grafana-admin
        userKey: admin-user
        passwordKey: admin-password
      service:
        type: NodePort
        nodePort: 30090
      persistence:
        enabled: false
      defaultDashboardsEnabled: true
      defaultDashboardsTimezone: utc
      nodeSelector:
        eks.amazonaws.com/nodegroup: system

    alertmanager:
      enabled: false

    nodeExporter:
      enabled: true

    kubeStateMetrics:
      enabled: true

    prometheusOperator:
      nodeSelector:
        eks.amazonaws.com/nodegroup: system
    EOT
  ]

  depends_on = [
    kubectl_manifest.monitoring_namespace,
    kubectl_manifest.grafana_admin_secret
  ]
}
