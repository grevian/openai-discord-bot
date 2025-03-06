package main

import (
	"context"
	"log"
	"log/slog"
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
	logger := config.GetLogger()

	logger.Info("Creating discord client")
	discordSession, err := config.GetDiscordSession()
	if err != nil {
		log.Fatal("Failed to instantiate Discord client", err)
	}
	err = discordSession.Open()
	if err != nil {

		log.Fatal("Failed to connect to Discord", err)
	}
	defer func() {
		err = discordSession.Close()
		if err != nil {
			logger.Error("Error closing Discord connection", slog.Any("error", err))
		}
	}()

	logger.Info("connecting to OpenAI")
	openapiClient, err := config.GetOpenAISession()
	if err != nil {
		log.Fatal("Failed to instantiate OpenAPI client", zap.Error(err))
	}

	botInstance := bot.NewAIBot(serviceCtx, openapiClient, discordSession, config.GetStorage(), config.GetImageStorage())

	logger.Info("Starting bot")
	err = botInstance.Go()
	if err != nil {
		log.Fatal("Failed to run the bot!", zap.Error(err))
	}

	stop := make(chan os.Signal)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	logger.Info("Gracefully shutting down")
	botInstance.Shutdown()
	time.Sleep(time.Second * 1)

	// Terminate the service context, which should flush any open logs/traces/etc.
	cancel()

	// Give anything flushing from the system context, a few seconds to finish up, then exit
	time.Sleep(time.Second * 5)
}
