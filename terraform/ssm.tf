resource "aws_ssm_parameter" "discord_token" {
  name        = "/${local.service}/${local.environment}/discord_token"
  description = "Discord bot token"
  type        = "SecureString"
  value       = "populated-out-of-band"

  lifecycle {
    ignore_changes = [value]
  }
}

resource "aws_ssm_parameter" "openai_token" {
  name        = "/${local.service}/${local.environment}/openai_token"
  description = "OpenAI API token"
  type        = "SecureString"
  value       = "populated-out-of-band"

  lifecycle {
    ignore_changes = [value]
  }
}

resource "aws_ssm_parameter" "honeycomb_api_key" {
  name        = "/${local.service}/${local.environment}/honeycomb_api_key"
  description = "Honeycomb ingest API key, substituted into adot/otel-agent-config.yaml at runtime"
  type        = "SecureString"
  value       = "populated-out-of-band"

  lifecycle {
    ignore_changes = [value]
  }
}
