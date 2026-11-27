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
#   BOT_DISCORD_TOKEN     - injected by danbot-prepare.sh from SSM Parameter Store
#   BOT_OPENAI_AUTH_TOKEN - injected by danbot-prepare.sh from SSM Parameter Store
# Locally, set these in your shell or a .env file consumed by your runner.

# Soft memory limit for the Go runtime: past it the GC runs harder instead of
# growing the heap. The bot idles at about 10 MB of heap and three drawings in
# flight peak around 40 MB, so this only bites under a burst, and keeps RSS
# down on a small instance. It is soft: the bot never fails for crossing it.
# Override with -e GOMEMLIMIT=... at docker run.
ENV GOMEMLIMIT=64MiB

WORKDIR /app
COPY --from=build /service-bin .
COPY prompts ./prompts

CMD ["/app/service-bin"]
