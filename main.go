package main

import (
	"context"
	"os"
	"os/signal"
	"time"

	"go.uber.org/zap"
	"openai-discord-bot/bot"
	"openai-discord-bot/config"
)

func main() {
	serviceCtx, cancel := context.WithCancel(context.Background())
	config.Configure(serviceCtx)
	log := config.GetLogger()

	log.Info("connecting to Discord")
	discordSession, err := config.GetDiscordSession()
	if err != nil {
		log.Fatal("Failed to instantiate Discord client", zap.Error(err))
	}

	log.Info("connecting to OpenAI")
	openapiClient, err := config.GetOpenAISession()
	if err != nil {
		log.Fatal("Failed to instantiate OpenAPI client", zap.Error(err))
	}

	botInstance := bot.NewAIBot(serviceCtx, openapiClient, discordSession, config.GetStorage(), config.GetImageStorage(), log)

	log.Info("Starting bot")
	err = botInstance.Go()
	if err != nil {
		log.Fatal("Failed to run the bot!", zap.Error(err))
	}

	stop := make(chan os.Signal)
	signal.Notify(stop, os.Interrupt)
	<-stop
	cancel()

	log.Info("Gracefully shutting down")
	err = discordSession.Close()
	if err != nil {
		log.Error("Error closing discord session", zap.Error(err))
	}

	// Give anything flushing from the system context, a few seconds to finish up
	time.Sleep(time.Second * 5)
	log.Sync()
}
