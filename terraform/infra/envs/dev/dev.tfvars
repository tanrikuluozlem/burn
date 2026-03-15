# terraform init -backend-config=envs/dev/aws.tfbackend
# terraform apply -var-file=envs/dev/dev.tfvars

aws_region = "eu-central-1"
env        = "dev"
vpc_cidr   = "10.1.0.0/16"

vpc_public_subnets = {
  "eu-central-1a" = "10.1.1.0/24"
  "eu-central-1b" = "10.1.2.0/24"
}

vpc_private_subnets = {
  "eu-central-1a" = "10.1.10.0/24"
  "eu-central-1b" = "10.1.20.0/24"
}

kubernetes_version = "1.31"

node_groups = {
  "default" = {
    instance_type = "t3.small"
    min_size      = 1
    max_size      = 2
  }
}
