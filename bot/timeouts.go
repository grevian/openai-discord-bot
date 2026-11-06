package bot

import (
	"context"
	"sync"
	"time"

	"openai-discord-bot/bot/storage"

	"github.com/bwmarrin/discordgo"
)

// timeouts bounds how long the bot waits on each external service. A zero
// value means "no per-call limit" (the request deadline still applies).
type timeouts struct {
	// Request bounds the whole handling of one Discord event.
	Request time.Duration
	// OpenAIText bounds one Responses API call, including the SDK's retries.
	OpenAIText time.Duration
	// OpenAIImage bounds one image generate or edit call, including retries.
	OpenAIImage time.Duration
	// Storage bounds one DynamoDB or S3 call, or one source-image fetch.
	Storage time.Duration
	// Discord bounds one Discord REST call.
	Discord time.Duration
	// Gallery bounds one gallery publish (a handful of small S3 writes).
	Gallery time.Duration
	// LinkExtract bounds one page extraction (a Kagi Extract call). It must
	// be longer than kagiPageTimeout, the time Kagi is asked to take.
	LinkExtract time.Duration
	// TypingRefresh is how often the "typing" indicator is re-sent while a
	// request is being handled. Discord shows one for about 10 seconds.
	TypingRefresh time.Duration
}

func defaultTimeouts() timeouts {
	return timeouts{
		Request:       5 * time.Minute,
		OpenAIText:    90 * time.Second,
		OpenAIImage:   3 * time.Minute,
		Storage:       30 * time.Second,
		Discord:       30 * time.Second,
		Gallery:       20 * time.Second,
		LinkExtract:   25 * time.Second,
		TypingRefresh: 8 * time.Second,
	}
}

// withTimeout derives a context that expires after d, or one that is merely
// cancellable when d is not positive.
func withTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, d)
}

// The helpers below wrap the bot's external calls that need nothing more than
// a per-call deadline. Calls with streaming bodies or special handling bound
// their contexts at the call site.

// discordCtx returns a request option that bounds one Discord REST call.
func (b *AIBot) discordCtx(ctx context.Context) (discordgo.RequestOption, context.CancelFunc) {
	callCtx, cancel := withTimeout(ctx, b.timeouts.Discord)
	return discordgo.WithContext(callCtx), cancel
}

// sendMessage posts a plain text message to a Discord channel.
func (b *AIBot) sendMessage(ctx context.Context, channel, content string) error {
	opt, cancel := b.discordCtx(ctx)
	defer cancel()
	_, err := b.discord.ChannelMessageSend(channel, content, opt)
	return err
}

func (b *AIBot) addThreadMessage(ctx context.Context, thread, source, message string) error {
	callCtx, cancel := withTimeout(ctx, b.timeouts.Storage)
	defer cancel()
	return b.storage.AddThreadMessage(callCtx, thread, source, message)
}

func (b *AIBot) getThread(ctx context.Context, thread string) ([]storage.ConversationMessage, error) {
	callCtx, cancel := withTimeout(ctx, b.timeouts.Storage)
	defer cancel()
	return b.storage.GetThread(callCtx, thread)
}

type typingStopKey struct{}

// keepTyping shows the "typing" indicator in channel until the returned stop
// function is called (or ctx ends). It sends one immediately and then
// re-sends every timeouts.TypingRefresh. stop is idempotent and waits for the
// goroutine to exit, so no typing call is made after it returns. The returned
// context carries stop for stopTypingFromContext.
func (b *AIBot) keepTyping(ctx context.Context, channel string) (context.Context, func()) {
	typingCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer recoverHandler("keepTyping")
		interval := b.timeouts.TypingRefresh
		if interval <= 0 {
			interval = defaultTimeouts().TypingRefresh
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			opt, cancelCall := b.discordCtx(typingCtx)
			_ = b.discord.ChannelTyping(channel, opt)
			cancelCall()
			select {
			case <-typingCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}
	return context.WithValue(ctx, typingStopKey{}, stop), stop
}

// stopTypingFromContext stops the indicator started by keepTyping, if ctx
// came from it. It lets code deep in a handler stop it right after the final
// send.
func stopTypingFromContext(ctx context.Context) {
	if stop, ok := ctx.Value(typingStopKey{}).(func()); ok {
		stop()
	}
}
