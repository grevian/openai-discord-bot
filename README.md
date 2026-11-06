# openai-discord-bot

This is a silly discord to openai bot designed to amuse myself and my friends

## Interacting with the bot

Every request starts with an `@Danbot` mention. Say `@Danbot help` on its own for this list in the bot's voice, or see the [help page](https://sillybullshit.click/help/).

| You write | What happens |
| --- | --- |
| `@Danbot <anything>` | A chat reply in Danbot's persona |
| `🧵` anywhere in the message | The reply goes in a new thread (60 minute auto-archive) which keeps context between messages; you still have to `@Danbot` inside the thread |
| `@Danbot 🎨 <idea>` or `@Danbot draw me a picture of <idea>` | An AI-generated crayon stick-figure drawing, 1024x1024 |
| `@Danbot ✏️ <change>`, `edit: <change>` or `redraw <change>`, with an image attached (or as a reply to one of the bot's own drawings) | An edited version of that image, 1024x1024 |
| `@Danbot help` (the whole message) | A static help reply, no model call |

Notes:

- Draw triggers match anywhere in the message, case-insensitively; edit triggers (with an image) likewise. With an image, edit beats draw, which beats chat. Without one, a message that starts with an edit trigger gets the "need an image" reply (unless it also draws).
- The image to edit must be png, jpeg or webp and under 50MB. Without an image, an edit request gets a short reply saying one is needed.
- Generated images are archived and, when the gallery is enabled, shown in the [gallery](https://sillybullshit.click/gallery/) (newest 48).
- Slash commands are not supported yet.

![conversational interactions](https://user-images.githubusercontent.com/603334/230736019-528a4a65-f787-4a16-918f-43c2f0203ddd.png)

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

### Operator settings

These optional variables set the values that are specific to the operator's deployment. Unset or empty means the default shown, which is what the deployed bot uses.

| Variable | Default | Purpose |
| --- | --- | --- |
| `BOT_OPERATOR_CHANNEL_ID` | `1091532074495787049` | Discord channel ID (17–20 digits) that receives the "awake" and "shutting down" notices. |
| `BOT_IMAGE_BASE_URL` | `https://sillybullshit.click` | `https` origin that archived images are linked from. A trailing slash is removed. No query, fragment or credentials. |

The bot refuses to start if either value is invalid.

## Deployment

Infrastructure is managed by Terraform under [`terraform/`](terraform/). A single EC2 instance (Graviton, `t4g.nano` by default, Amazon Linux 2023) runs the bot in Docker, with an OpenTelemetry collector (`otelcol-contrib`) as a systemd service, in `ca-central-1`, fronted by CloudFront on `sillybullshit.click` for image fetches and the static pages: the homepage (`/`, from `gallery/home/`), `/gallery/` and `/help/`. The bot uploads those pages to the bucket on every start; a CloudFront Function (`terraform/clean-urls.js`) maps `/x/` to `/x/index.html` and redirects `/gallery` and `/help` to the slash form, so no URL needs `index.html`. To add a page, add its name to `PAGES` in that file. State lives in an S3 backend (`openai-discord-bot-tfstate-537108148763`) with native locking.

Pushes to `master` trigger [`.github/workflows/deploy.yml`](.github/workflows/deploy.yml), which OIDC-assumes a role with `AdministratorAccess`, builds and pushes the arm64 image to GitHub Container Registry (`ghcr.io`) tagged with the commit SHA and `latest`, writes the SHA to the `/openai-discord-bot/prod/image_tag` SSM parameter, and uses SSM Run Command to restart the `bot` systemd service on the instance, which pulls that tag. Terraform is not run by the deploy workflow; changes under `terraform/` need a manual run of the `Terraform` workflow (`workflow_dispatch`: plan, then apply) or:

```
cd terraform
terraform init
terraform apply
```

Secrets live in SSM SecureString parameters and are read by the instance at startup — values are populated out-of-band (not in Terraform state):

- `/openai-discord-bot/prod/discord_token`
- `/openai-discord-bot/prod/openai_token`
- `/openai-discord-bot/prod/honeycomb_api_key` (read by the collector start script)

## Attaching to Discord

You can authorize the currently deployed bot to your server with the following OAuth2 URL: https://discord.com/api/oauth2/authorize?client_id=1076924748124143727&permissions=395204176896&scope=bot

Sometimes the bot ends up in a discord role with the same name as a the bot, which makes @ messages directed at it quite confusing, after adding the bot I'd suggest going to your server settings, and looking for a role with the same name as the bot user, if you see it, rename it something else like "GPTChatBot"
