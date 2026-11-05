# openai-discord-bot

This is a silly discord to openai bot designed to amuse myself and my friends

## Interacting with the bot

In most circumstances you can just `@Danbot` and recieve a response, if you include 🧵 in your message the response will be in a thread, which will retain context between messages, though you still have to `@Danbot` inside the thread to get responses

![conversational interactions](https://user-images.githubusercontent.com/603334/230736019-528a4a65-f787-4a16-918f-43c2f0203ddd.png)

The bot also supports requests to `@Danbot draw me a picture of <something>` which will respond with a Dall-E generated picture as requested

![drawing interactions](https://user-images.githubusercontent.com/603334/230735886-4d869e36-919b-4f1c-8fab-3cfe5e6cf0cc.png)

## Running Locally

It's probably easiest to run this via the Dockerfile, just remember to set the 
necessary environment variables on startup 

```
docker build -t openaibot . && docker run --rm -it openaibot \
 -e BOT_TRACING=false \ 
 -e BOT_JSON_LOGS=false \
 -e BOT_OPENAI_AUTH_TOKEN=<redacted> \
 -e BOT_DISCORD_TOKEN=<redacted> \
 -e BOT_OPENAIDISCORDBOTIMAGES_NAME=<s3 bucket> \
 -e AI_DISCORD_BOT_CONVERSATIONS_NAME=<dynamodb_table_name>
```

## Deployment

Infrastructure is managed by Terraform under [`terraform/`](terraform/). A single EC2 instance (Graviton, `t4g.nano` by default, Amazon Linux 2023) runs the bot in Docker alongside an OpenTelemetry collector in `ca-central-1`, fronted by CloudFront on `sillybullshit.click` for image fetches. State lives in an S3 backend (`openai-discord-bot-tfstate-537108148763`) with native locking.

Pushes to `master` trigger [`.github/workflows/deploy.yml`](.github/workflows/deploy.yml), which OIDC-assumes a role with `AdministratorAccess`, builds and pushes the arm64 image to GitHub Container Registry (`ghcr.io`) tagged with the commit SHA and `latest`, writes the SHA to the `/openai-discord-bot/prod/image_tag` SSM parameter, and uses SSM Run Command to restart the `bot` systemd service on the instance, which pulls that tag. Terraform is not run by the deploy workflow. To change infrastructure manually:

```
cd terraform
terraform init
terraform apply
```

Secrets live in SSM SecureString parameters and are read by the instance at startup — values are populated out-of-band (not in Terraform state):

- `/openai-discord-bot/prod/discord_token`
- `/openai-discord-bot/prod/openai_token`
- `/openai-discord-bot/prod/otel_config` (ADOT collector YAML)

## Attaching to Discord

You can authorize the currently deployed bot to your server with the following OAuth2 URL: https://discord.com/api/oauth2/authorize?client_id=1076924748124143727&permissions=395204176896&scope=bot

Sometimes the bot ends up in a discord role with the same name as a the bot, which makes @ messages directed at it quite confusing, after adding the bot I'd suggest going to your server settings, and looking for a role with the same name as the bot user, if you see it, rename it something else like "GPTChatBot"
