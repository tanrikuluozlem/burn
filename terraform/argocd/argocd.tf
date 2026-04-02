resource "kubectl_manifest" "namespace" {
  yaml_body = yamlencode({
    apiVersion = "v1"
    kind       = "Namespace"
    metadata = {
      name = "argocd"
    }
  })
}

resource "helm_release" "argocd" {
  namespace  = "argocd"
  name       = "argocd"
  repository = "https://argoproj.github.io/argo-helm"
  chart      = "argo-cd"
  version    = "9.4.15"
  wait       = true

  values = [
    <<-EOT
    configs:
      cm:
        timeout.reconciliation: 30s
    server:
      service:
        type: NodePort
    EOT
  ]

  depends_on = [kubectl_manifest.namespace]
}

resource "kubectl_manifest" "app_burn" {
  yaml_body = yamlencode({
    apiVersion = "argoproj.io/v1alpha1"
    kind       = "Application"
    metadata = {
      name      = "burn"
      namespace = "argocd"
    }
    spec = {
      project = "default"
      source = {
        repoURL        = var.repo_url
        targetRevision = "HEAD"
        path           = "charts/burn"
        helm = {
          valueFiles = ["values.yaml"]
        }
      }
      destination = {
        server    = "https://kubernetes.default.svc"
        namespace = var.app_namespace
      }
      syncPolicy = {
        automated = {
          prune    = true
          selfHeal = true
        }
      }
    }
  })

  depends_on = [helm_release.argocd]
}
