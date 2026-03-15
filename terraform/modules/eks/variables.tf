variable "project" {
  type = string
}

variable "env" {
  type = string
}

variable "aws_region" {
  type = string
}

variable "kubernetes_version" {
  type = string
}

variable "eks_service_role_arn" {
  type = string
}

variable "public_subnet_ids" {
  type = list(string)
}

variable "private_subnet_ids" {
  type = list(string)
}

variable "node_groups" {
  type = map(object({
    instance_type = string
    min_size      = number
    max_size      = number
  }))
}
