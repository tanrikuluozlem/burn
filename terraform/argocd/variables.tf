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

variable "ecr_repository" {
  type        = string
  description = "ECR repository URL for burn image"
}

variable "image_tag" {
  type        = string
  description = "Image tag to deploy"
  default     = "latest"
}

