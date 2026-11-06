package main

import (
	"context"
	"log/slog"
	"time"
)

// Shutdown time budget. bot.service runs `docker stop -t 45 bot` and the
// container has --stop-timeout 45, so Docker sends SIGKILL 45s after SIGTERM.
// Every step below is a ceiling, not a delay: each returns as soon as its work
// is done, so an idle bot exits in well under a second.
//
//	step                        ceiling
//	in-flight message handlers  22s
//	in-flight gallery publishes  8s   (drain total: 30s)
//	goodbye to the operator      2s
//	Discord session close        3s
//	telemetry flush              3s   (exporters flush in parallel, 2s each)
//	worst case                  38s   (7s margin under 45s)
const (
	shutdownHandlersTimeout = 22 * time.Second
	shutdownGalleryTimeout  = 8 * time.Second
	shutdownGoodbyeTimeout  = 2 * time.Second
	shutdownCloseTimeout    = 3 * time.Second
	shutdownFlushTimeout    = 3 * time.Second
)

// shutdownSteps are the actions run at shutdown, in order.
type shutdownSteps struct {
	drainHandlers func(context.Context) error
	drainGallery  func(context.Context) error
	goodbye       func(context.Context)
	closeDiscord  func() error
	// cancel ends the service context, which starts the telemetry flush.
	cancel    func()
	waitFlush func(context.Context) error
}

// runShutdown runs each step under its own ceiling. A step that overruns is
// abandoned (and logged) so later steps still get their time.
func runShutdown(logger *slog.Logger, s shutdownSteps) {
	step := func(name string, d time.Duration, f func(context.Context) error) {
		ctx, cancel := context.WithTimeout(context.Background(), d)
		defer cancel()
		if err := f(ctx); err != nil {
			logger.Warn("shutdown step did not finish in time", slog.String("step", name), slog.Duration("ceiling", d), slog.Any("error", err))
		}
	}
	step("in-flight messages", shutdownHandlersTimeout, s.drainHandlers)
	step("gallery publishes", shutdownGalleryTimeout, s.drainGallery)
	step("goodbye", shutdownGoodbyeTimeout, func(ctx context.Context) error { s.goodbye(ctx); return ctx.Err() })
	step("discord close", shutdownCloseTimeout, func(ctx context.Context) error {
		// discordgo's Close takes no context; run it aside so a hung
		// websocket cannot hold the process past its ceiling.
		done := make(chan error, 1)
		go func() { done <- s.closeDiscord() }()
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	// Ending the service context triggers the telemetry flushes; wait for
	// them to report done rather than sleeping a fixed time.
	s.cancel()
	step("telemetry flush", shutdownFlushTimeout, s.waitFlush)
}
