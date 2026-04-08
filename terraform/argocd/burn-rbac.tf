resource "kubectl_manifest" "burn_cluster_role" {
  yaml_body = yamlencode({
    apiVersion = "rbac.authorization.k8s.io/v1"
    kind       = "ClusterRole"
    metadata = {
      name = "burn"
    }
    rules = [
      {
        apiGroups = [""]
        resources = ["nodes", "pods", "namespaces"]
        verbs     = ["get", "list"]
      },
      {
        apiGroups = ["metrics.k8s.io"]
        resources = ["nodes", "pods"]
        verbs     = ["get", "list"]
      }
    ]
  })
}

resource "kubectl_manifest" "burn_cluster_role_binding" {
  yaml_body = yamlencode({
    apiVersion = "rbac.authorization.k8s.io/v1"
    kind       = "ClusterRoleBinding"
    metadata = {
      name = "burn"
    }
    roleRef = {
      apiGroup = "rbac.authorization.k8s.io"
      kind     = "ClusterRole"
      name     = "burn"
    }
    subjects = [
      {
        kind      = "ServiceAccount"
        name      = "burn"
        namespace = var.app_namespace
      }
    ]
  })

  depends_on = [kubectl_manifest.burn_cluster_role]
}
