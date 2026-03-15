# terraform init -backend-config=envs/qa/aws.tfbackend
# terraform apply -var-file=envs/qa/qa.tfvars

aws_region = "eu-central-1"
env        = "qa"
vpc_cidr   = "10.2.0.0/16"

vpc_public_subnets = {
  "eu-central-1a" = "10.2.1.0/24"
  "eu-central-1b" = "10.2.2.0/24"
}

vpc_private_subnets = {
  "eu-central-1a" = "10.2.10.0/24"
  "eu-central-1b" = "10.2.20.0/24"
}

kubernetes_version = "1.31"

node_groups = {
  "default" = {
    instance_type = "t3.small"
    min_size      = 1
    max_size      = 2
  }
}
