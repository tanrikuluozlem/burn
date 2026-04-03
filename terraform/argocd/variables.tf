variable "aws_region" {
  type    = string
  default = "eu-central-1"
}

variable "repo_url" {
  type    = string
  default = "https://github.com/tanrikuluozlem/burn"
}

variable "app_namespace" {
  type        = string
  description = "Namespace where burn app will be deployed"
}

