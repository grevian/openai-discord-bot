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

// shutdownDrainTimeout bounds how long shutdown waits for in-flight messages.
const shutdownDrainTimeout = 30 * time.Second

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

	botInstance := bot.NewAIBot(serviceCtx, cancel, openapiClient, discordSession, config.GetStorage(), config.GetImageStorage())

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
	// Stop taking new messages and let in-flight ones finish. Note that
	// `docker stop` (bot.service ExecStop) sends SIGKILL after its own grace
	// period (10s by default), which cuts this budget short unless it is
	// raised with `-t`.
	drainCtx, drainCancel := context.WithTimeout(context.Background(), shutdownDrainTimeout)
	if err := botInstance.Drain(drainCtx); err != nil {
		logger.Warn("Gave up waiting for in-flight messages", slog.Any("error", err))
	}
	drainCancel()
	botInstance.Shutdown()
	if err := discordSession.Close(); err != nil {
		logger.Error("Error closing Discord connection", slog.Any("error", err))
	}

	// Terminate the service context, which should flush any open logs/traces/etc.
	cancel()

	// Give anything flushing from the system context, a few seconds to finish up, then exit
	time.Sleep(time.Second * 5)
}
