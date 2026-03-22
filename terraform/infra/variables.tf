variable "aws_region" {
  type = string
}

variable "env" {
  type = string
}

variable "vpc_cidr" {
  type = string
}

variable "vpc_public_subnets" {
  type = map(string)
}

variable "vpc_private_subnets" {
  type = map(string)
}

variable "kubernetes_version" {
  type = string
}

variable "node_groups" {
  type = map(object({
    instance_type = string
    capacity_type = string
    min_size      = number
    max_size      = number
  }))
}
