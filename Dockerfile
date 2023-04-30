FROM golang:1.19-alpine AS build

WORKDIR /app

COPY ./go.mod ./go.mod
COPY ./go.sum ./go.sum

RUN go mod download

COPY . /app

RUN go build -o /service-bin

FROM alpine

ENV BOT_OPENAI_AUTH_TOKEN=""
ENV BOT_DISCORD_TOKEN=""

WORKDIR /app
COPY --from=build /service-bin .
COPY prompts ./prompts

CMD ["/app/service-bin"]
