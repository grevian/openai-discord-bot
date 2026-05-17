variable "image_tag" {
  description = "Container image tag deployed by the GHA workflow. Override per-deploy."
  type        = string
  default     = "latest"
}

variable "desired_count" {
  description = "ECS service desired task count."
  type        = number
  default     = 1
}
