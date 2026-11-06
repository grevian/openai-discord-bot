variable "instance_type" {
  description = "EC2 instance type for the bot. ARM/Graviton. t4g.micro (1 GiB): a t4g.nano (512 MiB) hit OOM kills with otelcol-contrib 0.164 (2026-11-06)."
  type        = string
  default     = "t4g.micro"
}

variable "otel_collector_version" {
  description = "Version of opentelemetry-collector-contrib to install on the instance."
  type        = string
  default     = "0.164.0"
}
