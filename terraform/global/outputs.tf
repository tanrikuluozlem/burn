output "ecr_repository_url" {
  value = aws_ecr_repository.burn.repository_url
}

output "ecr_repository_arn" {
  value = aws_ecr_repository.burn.arn
}

output "eks_cluster_role_arn" {
  value = aws_iam_role.eks_cluster.arn
}

output "github_oidc_provider_arn" {
  value = aws_iam_openid_connect_provider.github.arn
}

output "github_pricing_role_arn" {
  value = aws_iam_role.github_actions_pricing.arn
}
