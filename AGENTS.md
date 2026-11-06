# AGENTS.md

Discord bot (Go) that chats and draws via OpenAI in a gloomy persona. Code is in `bot/`, the public gallery site in `gallery/site/`, persona text in `prompts/danbo.json`, infrastructure in `terraform/` (changes need the owner's OK; secrets live in SSM).

## Keep the docs in sync

When a trigger, syntax or limit changes (`classifyMessage`, `maxEditSourceBytes`, image size, user-facing messages), update all of these in the same PR:

1. the usage section of `README.md`
2. the `@Danbot help` reply (`bot/help.go`)
3. `gallery/site/help.html` (also served at `/help/`)

## Checks

`go test -race ./...`, `golangci-lint run` (v2.15.0), `govulncheck ./...`.
