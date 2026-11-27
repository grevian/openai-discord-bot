package bot

import (
	"context"
	"log/slog"
	"sync"

	"github.com/bwmarrin/discordgo"
	"go.opentelemetry.io/otel"
)

func (b *AIBot) ReadyHandler(_ *discordgo.Session, _ *discordgo.Ready) {
	defer recoverHandler("ReadyHandler")
	slog.Default().WithGroup("ReadyHandler").Info("Connection state ready, Registering intents")
	// Ready fires again on every reconnect; only announce the first one.
	b.readyOnce.Do(func() {
		err := b.sendMessage(context.Background(), b.operatorChannelID, "Gah! I'm awake! How long was I out?!")
		if err != nil {
			slog.Default().Error("Failed to notify operator of the ready event", slog.Any("error", err))
		}
	})
}

// beginRequest admits one handler and returns the context it should derive
// from. It returns false once draining has begun; otherwise the caller must
// call b.inflight.Done when finished.
func (b *AIBot) beginRequest() (context.Context, bool) {
	b.lifeMu.Lock()
	defer b.lifeMu.Unlock()
	if b.draining {
		return nil, false
	}
	if b.handlerCtx == nil {
		b.handlerCtx, b.handlerCancel = context.WithCancel(context.Background())
	}
	b.inflight.Add(1)
	return b.handlerCtx, true
}

// Drain stops the bot accepting new messages and waits for in-flight handlers
// to finish, or for ctx to expire, in which case the remaining handlers'
// contexts are cancelled so they stop. It returns ctx's error if handlers
// were still running.
func (b *AIBot) Drain(ctx context.Context) error {
	b.lifeMu.Lock()
	b.draining = true
	b.lifeMu.Unlock()

	return waitOrCancel(ctx, &b.inflight, func() {
		b.lifeMu.Lock()
		defer b.lifeMu.Unlock()
		if b.handlerCancel != nil {
			b.handlerCancel()
		}
	})
}

// DrainGallery waits for gallery publishes that are already running, or for
// ctx to expire. Publishes only start inside an admitted handler, so it first
// waits for the handlers to finish too: after a Drain timeout, cancelled
// handlers may still be winding down and can start a publish (WaitGroup.Add
// must not race Wait). They are best-effort, but a deploy should not drop the
// image it was just handed. It returns ctx's error if work was still running.
func (b *AIBot) DrainGallery(ctx context.Context) error {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		b.inflight.Wait()
		b.galleryWG.Wait()
	}()
	return waitOrCancel(ctx, &wg, nil)
}

// waitOrCancel waits for wg or ctx. On ctx expiry it calls onTimeout (if any)
// and returns ctx's error. It never sleeps: an already-finished wg returns at once.
func waitOrCancel(ctx context.Context, wg *sync.WaitGroup, onTimeout func()) error {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		if onTimeout != nil {
			onTimeout()
		}
		return ctx.Err()
	}
}

// Shutdown tells the operator the bot is going away. ctx bounds the goodbye
// message; the bot context is not used for it as it may already be cancelled.
func (b *AIBot) Shutdown(ctx context.Context) {
	err := b.sendMessage(ctx, b.operatorChannelID, "Here I go, shutting down again!")
	_, span := otel.GetTracerProvider().Tracer("AIBot").Start(b.botCtx, "Shutdown")
	span.RecordError(err)
	span.End()
}
