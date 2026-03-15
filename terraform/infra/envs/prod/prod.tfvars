# terraform init -backend-config=envs/prod/aws.tfbackend
# terraform apply -var-file=envs/prod/prod.tfvars
# terraform destroy -var-file=envs/prod/prod.tfvars

aws_region = "eu-central-1"
env        = "prod"
vpc_cidr   = "10.0.0.0/16"

vpc_public_subnets = {
  "eu-central-1a" = "10.0.1.0/24"
  "eu-central-1b" = "10.0.2.0/24"
}

vpc_private_subnets = {
  "eu-central-1a" = "10.0.10.0/24"
  "eu-central-1b" = "10.0.20.0/24"
}

kubernetes_version = "1.31"

node_groups = {
  "default" = {
    instance_type = "t3.small"
    min_size      = 1
    max_size      = 3
  }
}
