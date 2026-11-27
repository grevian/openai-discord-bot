package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"openai-discord-bot/bot"
	"openai-discord-bot/config"
	"openai-discord-bot/health"
	"openai-discord-bot/telemetry"

	"github.com/bwmarrin/discordgo"
)

func main() {
	serviceCtx, cancel := context.WithCancel(context.Background())
	cfg, err := config.Load(os.LookupEnv)
	if err != nil {
		slog.Error("Invalid configuration", slog.Any("error", err))
		os.Exit(1)
	}
	awscfg, err := config.Configure(serviceCtx, cfg)
	if err != nil {
		slog.Error("Failed to configure the service", slog.Any("error", err))
		os.Exit(1)
	}
	logger := config.GetLogger()

	// The health endpoint starts early so it answers "starting" (503) while
	// Discord connects. The signals are filled in as the pieces come up.
	var (
		ready     atomic.Bool
		session   atomic.Pointer[discordgo.Session]
		botHandle atomic.Pointer[bot.AIBot]
	)
	if cfg.HealthAddr != "" {
		err := health.Serve(serviceCtx, cfg.HealthAddr, health.Handler(health.Sources{
			Version: telemetry.Version,
			Start:   time.Now(),
			Ready:   ready.Load,
			DiscordConnected: func() bool {
				s := session.Load()
				// Open holds the session lock while it connects, so a plain
				// RLock would hang the handler: TryRLock failing means
				// "not connected (yet)".
				if s == nil || !s.TryRLock() {
					return false
				}
				defer s.RUnlock()
				return s.DataReady
			},
			Degraded: func() bool { b := botHandle.Load(); return b != nil && b.Degraded() },
			Draining: func() bool { b := botHandle.Load(); return b != nil && b.Draining() },
		}))
		if err != nil {
			logger.Error("health endpoint unavailable, continuing without it", slog.String("addr", cfg.HealthAddr), slog.String("error", err.Error()))
		} else {
			logger.Info("health endpoint listening", slog.String("addr", cfg.HealthAddr))
		}
	}

	fatal := func(msg string, err error) {
		logger.Error(msg, slog.String("error", err.Error()))
		os.Exit(1)
	}

	logger.Info("connecting to OpenAI")
	openapiClient, err := config.GetOpenAISession(cfg)
	if err != nil {
		fatal("Failed to instantiate OpenAI client", err)
	}

	logger.Info("Creating discord client")
	discordSession, err := config.GetDiscordSession(cfg)
	if err != nil {
		fatal("Failed to instantiate Discord client", err)
	}
	session.Store(discordSession)
	if err = discordSession.Open(); err != nil {
		fatal("Failed to connect to Discord", err)
	}

	convStorage, err := config.GetStorage(awscfg, cfg)
	if err != nil {
		fatal("Failed to set up conversation storage", err)
	}

	// Feature flags: loaded now and every minute in the background. Until the
	// first load works (or if the table is unreachable) they keep their
	// defaults, which are the behaviour without any flag.
	flagCache := bot.NewFlags(config.GetFlagsBackend(awscfg, cfg))
	go flagCache.Run(serviceCtx, time.Minute)

	botInstance, err := bot.NewAIBot(serviceCtx, bot.Options{
		OpenAI:       openapiClient,
		Discord:      discordSession,
		Storage:      convStorage,
		ImageStorage: config.GetImageStorage(awscfg, cfg),
		Settings:     cfg.Bot,
		Flags:        flagCache,
		PromptPath:   cfg.PromptPath,
	})
	if err != nil {
		fatal("Failed to create the bot", err)
	}

	if g := config.GetGallery(awscfg, cfg); g != nil {
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

	botHandle.Store(botInstance)
	ready.Store(true)
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
