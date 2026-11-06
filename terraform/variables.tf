variable "instance_type" {
  description = "EC2 instance type for the bot. ARM/Graviton."
  type        = string
  default     = "t4g.nano"
}

variable "otel_collector_version" {
  description = "Version of opentelemetry-collector-contrib to install on the instance. Held at 0.119.0 (the last version that ran the bot) until a newer one is verified on a t4g.nano: 0.164.0 was never live before the 2026-11-06 outage."
  type        = string
  default     = "0.119.0"
}
