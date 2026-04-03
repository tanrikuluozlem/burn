resource "kubectl_manifest" "external_secret_grafana" {
  yaml_body = yamlencode({
    apiVersion = "external-secrets.io/v1beta1"
    kind       = "ExternalSecret"
    metadata = {
      name      = "grafana-admin"
      namespace = "monitoring"
    }
    spec = {
      refreshInterval = "1h"
      secretStoreRef = {
        name = "aws-secrets-manager"
        kind = "ClusterSecretStore"
      }
      target = {
        name = "grafana-admin"
      }
      data = [
        {
          secretKey = "admin-user"
          remoteRef = {
            key      = "burn/grafana"
            property = "username"
          }
        },
        {
          secretKey = "admin-password"
          remoteRef = {
            key      = "burn/grafana"
            property = "password"
          }
        }
      ]
    }
  })

  depends_on = [
    kubectl_manifest.cluster_secret_store,
    kubectl_manifest.monitoring_namespace
  ]
}

resource "kubectl_manifest" "external_secret_burn" {
  yaml_body = yamlencode({
    apiVersion = "external-secrets.io/v1beta1"
    kind       = "ExternalSecret"
    metadata = {
      name      = "burn-secrets"
      namespace = var.app_namespace
    }
    spec = {
      refreshInterval = "1h"
      secretStoreRef = {
        name = "aws-secrets-manager"
        kind = "ClusterSecretStore"
      }
      target = {
        name = "burn-secrets"
      }
      data = [
        {
          secretKey = "slack-webhook-url"
          remoteRef = {
            key      = "burn/slack"
            property = "webhook-url"
          }
        },
        {
          secretKey = "anthropic-api-key"
          remoteRef = {
            key      = "burn/anthropic"
            property = "api-key"
          }
        }
      ]
    }
  })

  depends_on = [kubectl_manifest.cluster_secret_store]
}
