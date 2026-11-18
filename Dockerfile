FROM --platform=$BUILDPLATFORM golang:1.27.4-alpine AS build

ARG TARGETOS
ARG TARGETARCH
# Git sha of the build, reported as deploy.version in telemetry.
ARG VERSION=dev

WORKDIR /app

COPY ./go.mod ./go.mod
COPY ./go.sum ./go.sum

RUN go mod download

COPY . /app

RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -ldflags "-X openai-discord-bot/telemetry.Version=${VERSION}" -o /service-bin

FROM alpine

# Required at runtime (NOT baked into the image):
#   BOT_DISCORD_TOKEN     - injected by start-bot.sh from SSM Parameter Store
#   BOT_OPENAI_AUTH_TOKEN - injected by start-bot.sh from SSM Parameter Store
# Locally, set these in your shell or a .env file consumed by your runner.

WORKDIR /app
COPY --from=build /service-bin .
COPY prompts ./prompts

CMD ["/app/service-bin"]
