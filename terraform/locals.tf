locals {
  service     = "openai-discord-bot"
  environment = "prod"
  name_prefix = "${local.service}-${local.environment}"
  domain      = "sillybullshit.click"

  log_retention = 30

  github_owner = "grevian"
  image_repo   = "ghcr.io/${local.github_owner}/${local.service}"
}
