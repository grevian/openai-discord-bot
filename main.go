package main

import (
	"context"
	"os"
	"os/signal"

	"go.uber.org/zap"
	"openai-discord-bot/bot"
	"openai-discord-bot/config"
)

func main() {
	config.Configure()
	log := config.GetLogger()

	discordSession, err := config.GetDiscordSession()
	if err != nil {
		log.Fatal("Failed to instantiate Discord client", zap.Error(err))
	}

	openapiClient, err := config.GetOpenAISession()
	if err != nil {
		log.Fatal("Failed to instantiate OpenAPI client", zap.Error(err))
	}

	botCtx := context.Background()

	botInstance := bot.NewAIBot(botCtx, openapiClient, discordSession, log)

	err = botInstance.Go()
	if err != nil {
		log.Fatal("Failed to run the bot!", zap.Error(err))
	}

	stop := make(chan os.Signal)
	signal.Notify(stop, os.Interrupt)
	<-stop

	log.Info("Gracefully shutting down")
}
