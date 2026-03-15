terraform {
  required_version = ">= 1.10"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }

  backend "s3" {}
}

provider "aws" {
  region = var.aws_region
}

data "terraform_remote_state" "global" {
  backend = "s3"

  config = {
    bucket = "burn-tfstate-prod"
    key    = "burn-global"
    region = "eu-central-1"
  }
}
