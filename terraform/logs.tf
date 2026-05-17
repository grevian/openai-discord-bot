resource "aws_cloudwatch_log_group" "service" {
  name              = "/${local.service}/${local.environment}"
  retention_in_days = local.log_retention
}
