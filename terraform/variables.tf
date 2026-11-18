variable "instance_type" {
  description = "EC2 instance type for the bot. ARM/Graviton. t4g.micro (1 GiB): a t4g.nano (512 MiB) hit OOM kills with otelcol-contrib 0.164 (2026-11-06)."
  type        = string
  default     = "t4g.micro"
}

# The collector is the minimal ocb build from otelcol/, uploaded by the
# Collector workflow to
# s3://<artifacts bucket>/artifacts/otelcol-danbot/<version>/otelcol-danbot-linux-arm64.
# Take both values from that workflow run's summary. The instance refuses a
# binary whose sha256 differs from otelcol_sha256.
variable "otelcol_version" {
  description = "dist.version of the otelcol-danbot build (otelcol/builder-config.yaml) to install on the instance."
  type        = string
  default     = "0.164.0-danbot.1"
}

variable "otelcol_sha256" {
  description = "sha256 of otelcol-danbot-linux-arm64 for otelcol_version."
  type        = string
  default     = "f6c6422b8ab3a80e0cca4670ef5a73268eab4fd3487fdf9bd42700be5c1664be"

  validation {
    condition     = can(regex("^[0-9a-f]{64}$", var.otelcol_sha256))
    error_message = "otelcol_sha256 must be a lowercase hex sha256 digest."
  }
}
