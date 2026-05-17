variable "image_tag" {
  description = "Container image tag deployed by the GHA workflow. Override per-deploy."
  type        = string
  default     = "latest"
}

variable "desired_count" {
  description = "ECS service desired task count. Set to 0 during cutover, then 1."
  type        = number
  default     = 1
}

variable "post_cutover" {
  description = "Flip to true after copilot env delete: provisions the S3 bucket policy and Route53 alias once Copilot's CFN no longer manages them. This variable is migration scaffolding and should be removed after cutover succeeds."
  type        = bool
  default     = false
}
