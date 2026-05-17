FROM golang:1.26-alpine AS build

WORKDIR /app

COPY ./go.mod ./go.mod
COPY ./go.sum ./go.sum

RUN go mod download

COPY . /app

RUN go build -o /service-bin

FROM alpine

# Required at runtime (NOT baked into the image):
#   BOT_DISCORD_TOKEN     - injected in ECS via terraform/ssm.tf -> aws_ssm_parameter.discord_token
#   BOT_OPENAI_AUTH_TOKEN - injected in ECS via terraform/ssm.tf -> aws_ssm_parameter.openai_token
# Locally, set these in your shell or a .env file consumed by your runner.

WORKDIR /app
COPY --from=build /service-bin .
COPY prompts ./prompts

CMD ["/app/service-bin"]
