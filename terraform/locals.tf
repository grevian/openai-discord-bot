locals {
  service     = "openai-discord-bot"
  environment = "prod"
  name_prefix = "${local.service}-${local.environment}"
  domain      = "sillybullshit.click"

  cpu           = 256
  memory        = 512
  log_retention = 30
}
