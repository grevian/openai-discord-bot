terraform {
  required_version = ">= 1.11.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }
}

provider "aws" {
  region = "ca-central-1"

  default_tags {
    tags = {
      Project   = "openai-discord-bot"
      ManagedBy = "terraform"
    }
  }
}

provider "aws" {
  alias  = "useast1"
  region = "us-east-1"

  default_tags {
    tags = {
      Project   = "openai-discord-bot"
      ManagedBy = "terraform"
    }
  }
}
