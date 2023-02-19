FROM golang:1.19-alpine AS build

COPY . /app

WORKDIR /app

RUN go mod download

RUN go build -o /service-bin

FROM alpine

ENV BOT_OPENAI_AUTH_TOKEN=""
ENV BOT_DISCORD_TOKEN=""

WORKDIR /app
COPY --from=build /service-bin .
COPY prompts ./prompts

CMD ["/app/service-bin"]
