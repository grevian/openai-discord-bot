terraform {
  backend "s3" {
    bucket       = "openai-discord-bot-tfstate-537108148763"
    key          = "openai-discord-bot/terraform.tfstate"
    region       = "ca-central-1"
    encrypt      = true
    use_lockfile = true
  }
}
