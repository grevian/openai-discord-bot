FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build

ARG TARGETOS
ARG TARGETARCH

WORKDIR /app

COPY ./go.mod ./go.mod
COPY ./go.sum ./go.sum

RUN go mod download

COPY . /app

RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /service-bin

FROM alpine

# Required at runtime (NOT baked into the image):
#   BOT_DISCORD_TOKEN     - injected by start-bot.sh from SSM Parameter Store
#   BOT_OPENAI_AUTH_TOKEN - injected by start-bot.sh from SSM Parameter Store
# Locally, set these in your shell or a .env file consumed by your runner.

WORKDIR /app
COPY --from=build /service-bin .
COPY prompts ./prompts

CMD ["/app/service-bin"]
