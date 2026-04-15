resource "aws_iam_role" "github_actions" {
  name = "${local.project}-${local.env}-gh-oidc-role"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Action = "sts:AssumeRoleWithWebIdentity"
      Effect = "Allow"
      Principal = {
        Federated = data.terraform_remote_state.global.outputs.github_oidc_provider_arn
      }
      Condition = {
        StringEquals = {
          "token.actions.githubusercontent.com:aud" = "sts.amazonaws.com"
        }
        StringLike = {
          "token.actions.githubusercontent.com:sub" = "repo:tanrikuluozlem/burn:environment:${local.env}"
        }
      }
    }]
  })
}

resource "aws_iam_role_policy" "github_actions_ecr" {
  name = "ecr-access"
  role = aws_iam_role.github_actions.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect   = "Allow"
        Action   = ["ecr:GetAuthorizationToken"]
        Resource = "*"
      },
      {
        Effect = "Allow"
        Action = [
          "ecr:BatchCheckLayerAvailability",
          "ecr:GetDownloadUrlForLayer",
          "ecr:BatchGetImage",
          "ecr:PutImage",
          "ecr:InitiateLayerUpload",
          "ecr:UploadLayerPart",
          "ecr:CompleteLayerUpload"
        ]
        Resource = data.terraform_remote_state.global.outputs.ecr_repository_arn
      }
    ]
  })
}

resource "aws_iam_role_policy" "github_actions_eks" {
  name = "eks-access"
  role = aws_iam_role.github_actions.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = ["eks:DescribeCluster", "eks:ListClusters"]
      Resource = "*"
    }]
  })
}

resource "aws_eks_access_entry" "github_actions" {
  cluster_name  = data.terraform_remote_state.infra.outputs.cluster_name
  principal_arn = aws_iam_role.github_actions.arn
  type          = "STANDARD"
}

resource "aws_eks_access_policy_association" "github_actions" {
  cluster_name  = data.terraform_remote_state.infra.outputs.cluster_name
  policy_arn    = "arn:aws:eks::aws:cluster-access-policy/AmazonEKSAdminPolicy"
  principal_arn = aws_iam_role.github_actions.arn

  access_scope {
    type       = "namespace"
    namespaces = [local.env]
  }
}

data "aws_iam_policy_document" "burn_assume_role" {
  statement {
    actions = ["sts:AssumeRoleWithWebIdentity"]
    effect  = "Allow"

    condition {
      test     = "StringEquals"
      variable = "${replace(data.terraform_remote_state.infra.outputs.oidc_provider_url, "https://", "")}:sub"
      values   = ["system:serviceaccount:${local.env}:burn"]
    }

    condition {
      test     = "StringEquals"
      variable = "${replace(data.terraform_remote_state.infra.outputs.oidc_provider_url, "https://", "")}:aud"
      values   = ["sts.amazonaws.com"]
    }

    principals {
      identifiers = [data.terraform_remote_state.infra.outputs.oidc_provider_arn]
      type        = "Federated"
    }
  }
}

resource "aws_iam_role" "burn_irsa" {
  name               = "${local.project}-${local.env}-irsa"
  assume_role_policy = data.aws_iam_policy_document.burn_assume_role.json
}

data "aws_iam_policy_document" "burn_permissions" {
  statement {
    effect = "Allow"
    actions = [
      "pricing:GetProducts",
      "pricing:DescribeServices",
      "pricing:GetAttributeValues"
    ]
    resources = ["*"]
  }

  statement {
    effect = "Allow"
    actions = [
      "ec2:DescribeInstances",
      "ec2:DescribeRegions",
      "ec2:DescribeInstanceTypes"
    ]
    resources = ["*"]
  }
}

resource "aws_iam_policy" "burn" {
  name   = "${local.project}-${local.env}-policy"
  policy = data.aws_iam_policy_document.burn_permissions.json
}

resource "aws_iam_role_policy_attachment" "burn" {
  role       = aws_iam_role.burn_irsa.name
  policy_arn = aws_iam_policy.burn.arn
}
