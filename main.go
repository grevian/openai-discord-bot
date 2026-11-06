package main

import (
	"context"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

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

	logger.Info("connecting to OpenAI")
	openapiClient, err := config.GetOpenAISession()
	if err != nil {
		log.Fatal("Failed to instantiate OpenAPI client", slog.Any("error", err))
	}

	botInstance := bot.NewAIBot(serviceCtx, cancel, openapiClient, discordSession, config.GetStorage(), config.GetImageStorage(), config.GetBotSettings())

	if g := config.GetGallery(); g != nil {
		botInstance.SetGallery(g)
		// Deploying the bot deploys the gallery page. Best-effort and off the
		// startup path: a failure leaves the previous page in place.
		go func() {
			ctx, cancel := context.WithTimeout(serviceCtx, 30*time.Second)
			defer cancel()
			if err := g.PublishSite(ctx); err != nil {
				logger.Error("failed to publish the gallery page", slog.Any("error", err))
			}
		}()
	}

	logger.Info("Starting bot")

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	select {
	case <-stop:
		logger.Info("Received shutdown signal")
	case <-serviceCtx.Done():
		logger.Info("Service context cancelled (internal shutdown)")
	}

	logger.Info("Gracefully shutting down")
	// Stop taking new messages and let in-flight work finish (budget: shutdown.go).
	runShutdown(logger, shutdownSteps{
		drainHandlers: botInstance.Drain,
		drainGallery:  botInstance.DrainGallery,
		goodbye:       botInstance.Shutdown,
		closeDiscord:  discordSession.Close,
		cancel:        cancel,
		waitFlush:     config.WaitFlushed,
	})
}
