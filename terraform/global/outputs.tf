output "ecr_repository_url" {
  value = aws_ecr_repository.burn.repository_url
}

output "eks_cluster_role_arn" {
  value = aws_iam_role.eks_cluster.arn
}

output "github_actions_role_arns" {
  value = { for env in local.environments : env => aws_iam_role.github_actions[env].arn }
}
