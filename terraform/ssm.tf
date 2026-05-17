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

resource "aws_ssm_parameter" "otel_config" {
  name        = "/${local.service}/${local.environment}/otel_config"
  description = "ADOT collector config"
  type        = "SecureString"
  value       = "populated-out-of-band"

  lifecycle {
    ignore_changes = [value]
  }
}
