module "eks" {
  source = "../modules/eks"

  project              = local.project
  env                  = local.env
  aws_region           = var.aws_region
  kubernetes_version   = var.kubernetes_version
  eks_service_role_arn = data.terraform_remote_state.global.outputs.eks_cluster_role_arn
  public_subnet_ids    = [for s in aws_subnet.public : s.id]
  private_subnet_ids   = [for s in aws_subnet.private : s.id]
  node_groups          = var.node_groups
}
