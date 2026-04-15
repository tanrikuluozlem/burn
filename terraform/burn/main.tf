terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }
  required_version = "~> 1.0"
  backend "s3" {}
}

provider "aws" {
  region = var.aws_region
}

data "aws_caller_identity" "current" {}

data "terraform_remote_state" "global" {
  backend = "s3"
  config = {
    bucket = "burn-tfstate-prod"
    key    = "burn-global"
    region = "eu-central-1"
  }
}

data "terraform_remote_state" "infra" {
  backend = "s3"
  config = {
    bucket = "burn-tfstate-prod"
    key    = "burn-prod-infra"
    region = "eu-central-1"
  }
}
