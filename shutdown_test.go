package main

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"
)

func idleSteps() shutdownSteps {
	return shutdownSteps{
		drainHandlers: func(context.Context) error { return nil },
		drainGallery:  func(context.Context) error { return nil },
		goodbye:       func(context.Context) {},
		closeDiscord:  func() error { return nil },
		cancel:        func() {},
		waitFlush:     func(context.Context) error { return nil },
	}
}

// The ceilings are not delays: with nothing in flight shutdown is immediate.
func TestIdleShutdownIsFast(t *testing.T) {
	start := time.Now()
	runShutdown(slog.Default(), idleSteps())
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Errorf("idle shutdown took %v", d)
	}
}

func TestBudgetFitsDockerStop(t *testing.T) {
	total := shutdownHandlersTimeout + shutdownGalleryTimeout + shutdownGoodbyeTimeout + shutdownCloseTimeout + shutdownFlushTimeout
	if total > 40*time.Second {
		t.Errorf("worst case %v leaves under 5s margin in the 45s docker stop grace", total)
	}
}

// A hung Discord close is abandoned at its ceiling and the flush still runs.
func TestHungDiscordCloseDoesNotBlockFlush(t *testing.T) {
	s := idleSteps()
	release := make(chan struct{})
	defer close(release)
	s.closeDiscord = func() error { <-release; return errors.New("late") }
	flushed := false
	s.waitFlush = func(context.Context) error { flushed = true; return nil }
	start := time.Now()
	runShutdown(slog.Default(), s)
	if d := time.Since(start); d < shutdownCloseTimeout || d > shutdownCloseTimeout+time.Second {
		t.Errorf("took %v, want about %v", d, shutdownCloseTimeout)
	}
	if !flushed {
		t.Error("telemetry flush was skipped")
	}
}
