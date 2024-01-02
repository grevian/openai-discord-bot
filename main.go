package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"
	"openai-discord-bot/bot"
	"openai-discord-bot/config"
)

func main() {
	serviceCtx, cancel := context.WithCancel(context.Background())
	config.Configure(serviceCtx)
	log := config.GetLogger()

	log.Info("Creating discord client")
	discordSession, err := config.GetDiscordSession()
	if err != nil {
		log.Fatal("Failed to instantiate Discord client", zap.Error(err))
	}
	err = discordSession.Open()
	if err != nil {
		log.Fatal("Failed to connect to Discord", zap.Error(err))
	}
	defer func() {
		err = discordSession.Close()
		if err != nil {
			log.Error("Error closing Discord connection", zap.Error(err))
		}
	}()

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
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	log.Info("Gracefully shutting down")
	botInstance.Shutdown()
	time.Sleep(time.Second * 1)
	cancel()

	// Give anything flushing from the system context, a few seconds to finish up
	time.Sleep(time.Second * 5)
	_ = log.Sync()
}
