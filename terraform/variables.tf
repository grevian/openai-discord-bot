variable "instance_type" {
  description = "EC2 instance type for the bot. ARM/Graviton."
  type        = string
  default     = "t4g.nano"
}

variable "otel_collector_version" {
  description = "Version of opentelemetry-collector-contrib to install on the instance."
  type        = string
  default     = "0.119.0"
}
