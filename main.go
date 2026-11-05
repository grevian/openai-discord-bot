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

// Shutdown time budget. `docker stop` (bot.service ExecStop) sends SIGKILL
// after its default 10s grace period, so drain + goodbye + Discord close +
// telemetry flush must all fit under that: 8s + 0.7s + 0.8s = 9.5s.
// Raising the grace period (terraform) would allow a longer drain.
const (
	// shutdownDrainTimeout bounds how long shutdown waits for in-flight messages.
	shutdownDrainTimeout = 8 * time.Second
	// shutdownGoodbyeTimeout bounds the goodbye message to the operator.
	shutdownGoodbyeTimeout = 700 * time.Millisecond
	// shutdownFlushWait is the time given to flush telemetry after cancel.
	shutdownFlushWait = 800 * time.Millisecond
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
	// Stop taking new messages and let in-flight ones finish (see the budget above).
	drainCtx, drainCancel := context.WithTimeout(context.Background(), shutdownDrainTimeout)
	if err := botInstance.Drain(drainCtx); err != nil {
		logger.Warn("Gave up waiting for in-flight messages", slog.Any("error", err))
	}
	drainCancel()
	goodbyeCtx, goodbyeCancel := context.WithTimeout(context.Background(), shutdownGoodbyeTimeout)
	botInstance.Shutdown(goodbyeCtx)
	goodbyeCancel()
	if err := discordSession.Close(); err != nil {
		logger.Error("Error closing Discord connection", slog.Any("error", err))
	}

	// Terminate the service context, which should flush any open logs/traces/etc.
	cancel()

	// Give anything flushing from the service context a moment to finish, then exit.
	time.Sleep(shutdownFlushWait)
}
