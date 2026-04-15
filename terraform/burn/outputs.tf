output "github_actions_role_arn" {
  value = aws_iam_role.github_actions.arn
}

output "irsa_role_arn" {
  value = aws_iam_role.burn_irsa.arn
}
