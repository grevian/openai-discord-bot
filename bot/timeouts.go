package bot

import (
	"context"
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
}

func defaultTimeouts() timeouts {
	return timeouts{
		Request:     5 * time.Minute,
		OpenAIText:  90 * time.Second,
		OpenAIImage: 3 * time.Minute,
		Storage:     30 * time.Second,
		Discord:     30 * time.Second,
		Gallery:     20 * time.Second,
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
