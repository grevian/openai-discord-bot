package bot

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"openai-discord-bot/bot/feedback"
	"openai-discord-bot/telemetry"

	"github.com/bwmarrin/discordgo"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

const (
	maxFeedbackRunes      = 1500
	maxReplyContentRunes  = 500
	maxOperatorNoticeRune = 500
	feedbackLimitCount    = 5
	feedbackLimitWindow   = 10 * time.Minute
)

// Static replies for the feedback trigger. No model is called for them.
const (
	msgFeedbackThanks   = "Fine, I wrote it down. The owner will read it eventually. Nobody will do anything about it, but it's written down."
	msgIdeaThanks       = "An idea. I wrote it down for the owner. It will probably sit in a table, forgotten, like all the best ideas."
	msgFeedbackOff      = "I'd write that down, but nobody has switched feedback on here. Tell the owner, I suppose."
	msgFeedbackUsage    = "Feedback about what? Say `@Danbot feedback <text>` or `@Danbot idea <text>`."
	msgFeedbackSlowDown = "That's a lot of feedback. I've written down enough for now. Try again in a few minutes."
	msgFeedbackFailed   = "Ugh, I tried to write that down and the pen broke. Please try again later."
)

// handleFeedback stores the text of a feedback or idea message, thanks the
// user and tells the operator channel. No model is called, and the exchange
// is not added to the conversation thread.
func (b *AIBot) handleFeedback(ctx context.Context, responseChannel string, intent messageIntent, text string, m *discordgo.MessageCreate) error {
	ctx, span := otel.GetTracerProvider().Tracer("AIBot").Start(ctx, "feedback")
	defer span.End()
	kind := intent.String()
	span.SetAttributes(attribute.String("feedback.kind", kind))

	if b.feedback == nil {
		return b.sendMessage(ctx, responseChannel, msgFeedbackOff)
	}
	if strings.TrimSpace(text) == "" {
		return b.sendMessage(ctx, responseChannel, msgFeedbackUsage)
	}
	if !b.feedbackLimiter.Allow(m.Author.ID) {
		span.SetAttributes(attribute.Bool("feedback.rate_limited", true))
		return b.sendMessage(ctx, responseChannel, msgFeedbackSlowDown)
	}

	text, truncated := truncateRunes(text, maxFeedbackRunes)
	span.SetAttributes(attribute.Int("feedback.chars", len([]rune(text))))
	// The message's own timestamp, so a redelivery of the same message gets
	// the same sort key and the store's conditional put drops the duplicate.
	created := m.Timestamp
	if created.IsZero() {
		created = time.Now()
	}
	it := feedback.Item{
		Kind:       kind,
		Text:       text,
		Truncated:  truncated,
		CreatedAt:  created,
		UserID:     m.Author.ID,
		Username:   m.Author.Username,
		GuildID:    m.GuildID,
		ChannelID:  m.ChannelID,
		MessageID:  m.ID,
		BotVersion: telemetry.Version,
	}
	if ref := b.referencedMessage(ctx, m); ref != nil && ref.Author != nil && ref.Author.ID == b.discord.BotUser().ID {
		it.ReplyToMessageID = ref.ID
		it.ReplyToContent, _ = truncateRunes(ref.Content, maxReplyContentRunes)
	}

	putCtx, cancel := withTimeout(ctx, b.timeouts.Storage)
	err := b.feedback.Put(putCtx, it)
	cancel()
	if err != nil {
		span.SetAttributes(attribute.Bool("feedback.stored", false))
		return newUserError(msgFeedbackFailed, err)
	}
	span.SetAttributes(attribute.Bool("feedback.stored", true))
	slog.InfoContext(ctx, "feedback recorded", slog.String("kind", kind), slog.String("id", it.SortKey()), slog.String("user", it.UserID))

	thanks := msgFeedbackThanks
	if kind == feedback.KindIdea {
		thanks = msgIdeaThanks
	}
	stopTypingFromContext(ctx)
	if err := b.sendMessage(ctx, responseChannel, thanks); err != nil {
		return fmt.Errorf("failed to send feedback reply: %w", err)
	}

	// The notice is best effort and must not fail the request.
	if b.operatorChannelID != "" {
		if err := b.sendMessageNoMentions(context.WithoutCancel(ctx), b.operatorChannelID, operatorNotice(it)); err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "operator notice failed")
			slog.WarnContext(ctx, "failed to send the feedback notice", slog.Any("error", err))
		}
	}
	return nil
}

// operatorNotice is the message posted to the operator channel for an item.
func operatorNotice(it feedback.Item) string {
	quoted, cut := truncateRunes(it.Text, maxOperatorNoticeRune)
	if cut {
		quoted += "…"
	}
	quoted = "> " + strings.ReplaceAll(quoted, "\n", "\n> ")
	return fmt.Sprintf("📝 %s from %s (%s)\n%s\n%s id `%s`", it.Kind, it.Username, it.UserID, quoted,
		feedback.MessageURL(it.GuildID, it.ChannelID, it.MessageID), it.SortKey())
}
