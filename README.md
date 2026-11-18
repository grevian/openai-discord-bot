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
| `@Danbot <https link>` on its own, `@Danbot 📰 <link>` or `@Danbot summarize <link>` | A one-paragraph reaction to the page in Danbot's voice, more mood than detail (needs `BOT_KAGI_API_KEY`) |
| `@Danbot help` (the whole message) | A static help reply, no model call |

Notes:

- Add `🫧` to a draw or edit request (in the message text, not as a reaction) for a clean image: your text goes to the image model without Danbot's style or edit framing. Size, the safety identifier and, for edits, the thread context flag still apply. The bot's reply to a clean image carries a `🫧`, and an edit made as a reply to that image stays clean. An image you attach yourself needs its own `🫧`.
- Draw triggers match anywhere in the message, case-insensitively; edit triggers (with an image) likewise. With an image, edit beats draw, which beats chat. Without one, a message that starts with an edit trigger gets the "need an image" reply (unless it also draws).
- The image to edit must be png, jpeg or webp and under 50MB. Without an image, an edit request gets a short reply saying one is needed.
- Generated images are archived and, when the gallery is enabled, shown in the [gallery](https://sillybullshit.click/gallery/) (newest 48).
- Link summaries take `https` links only. The page is read by the Kagi Extract API, and only its first 4,000 characters are sent to the model. A link inside an ordinary chat message (`@Danbot is <link> any good?`) is just chat. Draw and edit beat summarise.
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

`BOT_TRACING` and `BOT_METRICS` (both default `true`) export traces and the bot's memory metrics over OTLP to the collector named by the standard `OTEL_EXPORTER_OTLP_*` variables. The metrics are the Go runtime set (`go.memory.used`, `go.memory.limit`, `go.goroutine.count`, ...) and `process.memory.usage` (RSS), every minute. Set either to `false` when there is no collector.

### Operator settings

These optional variables set the values that are specific to the operator's deployment. Unset or empty means the default shown, which is what the deployed bot uses.

| Variable | Default | Purpose |
| --- | --- | --- |
| `BOT_OPERATOR_CHANNEL_ID` | `1091532074495787049` | Discord channel ID (17–20 digits) that receives the "awake" and "shutting down" notices. |
| `BOT_IMAGE_BASE_URL` | `https://sillybullshit.click` | `https` origin that archived images are linked from. A trailing slash is removed. No query, fragment or credentials. |

The bot refuses to start if either value is invalid.

`BOT_OWNER_USER_ID` (optional) is the Discord user ID (17–20 digits) of the bot owner, the only user who may change feature flags. Unset or invalid (logged) turns the flag command off; the bot still starts. `BOT_FLAGS_TABLE` (optional) is the DynamoDB table that stores the flags. Unset means every flag stays at its default and can't be changed. In production both come from `danbot-prepare.sh` (see Deployment).

### Feature flags (owner only)

Feature flags switch bot behaviour on and off without a deploy. The owner sends commands to the bot **by direct message**, with or without an `@Danbot` mention:

| DM | What happens |
| --- | --- |
| `flags` | Lists every flag with its type, current value, default, who last changed it and when, and what it does |
| `flag set <name> <value>` | Changes one flag. Bools take `on`/`off` (also `true`/`false`, `yes`/`no`, `1`/`0`) |

- Only `BOT_OWNER_USER_ID` can use these. The same commands from anyone else are ignored and logged, with no reply. Flag commands don't work in servers. There they are ordinary chat.
- A change takes effect at once. The bot also reloads every flag from the table once a minute, so an edit made in the AWS console applies within a minute.
- Every change is logged, saved with an audit entry in the table (`pk = audit#<name>`), and announced in the operator channel.
- If the table is missing or unreachable, the bot runs on the defaults, or keeps the last values it loaded, and keeps working. Every default is the behaviour without the flag.

| Flag | Type | Default | Effect |
| --- | --- | --- | --- |
| `edit_thread_context` | bool | off | In a thread, an image edit also reads the thread's earlier user messages (the newest 10, at most 2,000 characters, text only, links removed). Outside a thread, an edit that replies to one of the bot's images reads the earlier requests of that reply chain the same way (the newest 20 messages of the chain, bot replies included). One short text call with the chat model (25 second limit) condenses them into at most 5 short drawing directives, and these go into the edit prompt after the requested change. If the call fails, times out or finds nothing, the edit runs as if the flag were off. Costs one extra text call and a little latency per edit in a thread or reply chain, plus up to about 20 Discord message fetches to walk a reply chain; an edit of an attached image outside a thread is unchanged. |

Flags are defined in `bot/flags.go`. To add one, register it there with a default that keeps today's behaviour.

`BOT_KAGI_API_KEY` (optional, a secret) is the [Kagi Extract API](https://kagi.com/api/docs) key used for link summaries. Unset means the summarise triggers reply that the feature isn't configured. In production, deploy.yml copies the `KAGI_API_KEY` GitHub secret into the SSM parameter `/openai-discord-bot/prod/kagi_api_key`, which the instance reads when the bot starts.

## Deployment

Infrastructure is managed by Terraform under [`terraform/`](terraform/). A single EC2 instance (Graviton, `t4g.micro` (1 GiB) by default, Amazon Linux 2023) runs the bot as a native binary under systemd (no Docker on the host), with a minimal OpenTelemetry collector (`otelcol-danbot`, built from [`otelcol/`](otelcol/) and fetched from a private artifacts bucket at boot) as a systemd service, in `ca-central-1`, fronted by CloudFront on `sillybullshit.click` for image fetches and the static pages: the homepage (`/`, from `gallery/home/`), `/gallery/` and `/help/`. The bot uploads those pages to the bucket on every start; a CloudFront Function (`terraform/clean-urls.js`) maps `/x/` to `/x/index.html` and redirects `/gallery` and `/help` to the slash form, so no URL needs `index.html`. To add a page, add its name to `PAGES` in that file. State lives in an S3 backend (`openai-discord-bot-tfstate-537108148763`) with native locking.

Pushes to `master` trigger [`.github/workflows/deploy.yml`](.github/workflows/deploy.yml), which OIDC-assumes a role with `AdministratorAccess`, builds and pushes the arm64 image to GitHub Container Registry (`ghcr.io`) tagged with the commit SHA and `latest` (published for people who want to run the bot in a container; production does not run it), builds the static arm64 binary with `scripts/build-release.sh` (the same `deploy.version` ldflags as the Dockerfile) and uploads it with its `.sha256` to the private artifacts bucket as `artifacts/danbot/<sha>/danbot-linux-arm64.tar.gz` (the binary plus `prompts/`), writes the SHA to the `/openai-discord-bot/prod/image_tag` SSM parameter (the name predates the native binary; it holds the release the instance runs), and uses SSM Run Command to restart the `bot` systemd service on the instance. Each start of the service runs `danbot-prepare.sh` as root: it fetches and checksum-verifies that release with `fetch-artifact` into `/opt/danbot/releases/<sha>/` (keeping the newest three), points `/opt/danbot/current` at it, and writes the secrets read from SSM to `/run/danbot/bot.env` (tmpfs, root-only, read by systemd as the unit's `EnvironmentFile`). The bot itself runs as the unprivileged `danbot` user under a sandboxed unit (`NoNewPrivileges`, `ProtectSystem=strict`, a system call filter, and more; see `terraform/user-data.sh.tftpl`) with `Restart=on-failure`.

**Rolling back** needs no rebuild: put an earlier SHA in the `image_tag` parameter (`aws ssm put-parameter --name /openai-discord-bot/prod/image_tag --type String --overwrite --value <sha>`) and run `systemctl restart bot` through SSM; any SHA that deploy.yml built has its artifact in the bucket. Inspect the instance with `journalctl -u bot` (through SSM Session Manager). Terraform is not run by the deploy workflow; changes under `terraform/` need a manual run of the `Terraform` workflow (`workflow_dispatch`: plan, then apply) or:

```
cd terraform
terraform init
terraform apply
```

Pull requests that change `terraform/**` run the [`Terraform Plan`](.github/workflows/terraform-plan.yml) workflow: `terraform plan -lock=false` only (never apply), posted as a PR comment (updated in place) and in the job summary. It assumes a separate read-only role (`openai-discord-bot-prod-tf-plan`, `terraform/plan_role.tf`: `ReadOnlyAccess` minus S3 object / DynamoDB item reads on the app's data), trusted only for `pull_request` runs of this repo, via the `AWS_PLAN_ROLE_TO_ASSUME` secret. It does not use the `production` environment or the apply role, and is skipped for fork and Dependabot PRs. **Bootstrap:** the role must exist first: merge, run the `Terraform` workflow once to apply it, then add the `plan_role_arn` output as the `AWS_PLAN_ROLE_TO_ASSUME` repository secret. Until then the plan job fails at the credentials step. `-lock=false` means a PR plan never blocks an apply; the plan can be slightly stale if an apply runs at the same time. The plan role can read the state, which holds the SSM parameter values, so run it only on PRs you trust.

The instance only reads its user-data (`danbot-prepare.sh`, the `bot.service` unit and its flags) when it first boots, and the ASG follows the launch template's `$Latest` version, so Terraform changes to the launch template do not reach the running instance by themselves. The [`Instance Refresh`](.github/workflows/instance-refresh.yml) workflow replaces the instance: it refuses to start while another refresh is running, starts a rolling refresh with `MinHealthyPercentage=0` and waits (20 minute limit) for it to finish, failing on any end state other than `Successful`. It runs automatically after the `Terraform` workflow when the apply changed `aws_launch_template.bot` (untick `refresh-instance` to skip), and can be run by hand (`workflow_dispatch`) for changes applied locally. **The bot is offline for a few minutes during a refresh**: with a single instance there is no spare capacity, and two bots on one Discord token would fight over the session. It needs `autoscaling:StartInstanceRefresh` and `autoscaling:DescribeInstanceRefreshes` on the OIDC role (`AWS_ROLE_TO_ASSUME`); the role is not defined in this repo's Terraform and has `AdministratorAccess`, so nothing needs adding unless it is ever narrowed.

Secrets live in SSM SecureString parameters and are read by the instance at startup — values are populated out-of-band (Terraform ignores their value, but the state holds whatever it last read):

- `/openai-discord-bot/prod/discord_token`
- `/openai-discord-bot/prod/openai_token`
- `/openai-discord-bot/prod/honeycomb_api_key` (read by the collector start script)

`/openai-discord-bot/prod/owner_user_id` is a plain `String` parameter (not a secret): the Discord user ID of the bot owner, passed to the bot as `BOT_OWNER_USER_ID`. Terraform creates it with the placeholder `populated-out-of-band`, which means unset (the owner-only flag command is off). Set it with `aws ssm put-parameter --name /openai-discord-bot/prod/owner_user_id --type String --overwrite --value <your user id>`; `danbot-prepare.sh` reads it on every bot restart, so a deploy picks up a change. Feature flags live in the DynamoDB table `openai-discord-bot-prod-flags`, passed as `BOT_FLAGS_TABLE`.

## Attaching to Discord

You can authorize the currently deployed bot to your server with the following OAuth2 URL: https://discord.com/api/oauth2/authorize?client_id=1076924748124143727&permissions=395204176896&scope=bot

Sometimes the bot ends up in a discord role with the same name as a the bot, which makes @ messages directed at it quite confusing, after adding the bot I'd suggest going to your server settings, and looking for a role with the same name as the bot user, if you see it, rename it something else like "GPTChatBot"
