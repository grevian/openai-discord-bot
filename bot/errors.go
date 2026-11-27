package bot

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/openai/openai-go/v3"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// isFatalOpenAIError reports whether err indicates a condition that won't
// recover on retry: an exhausted quota or a revoked / invalid API key. When
// true the caller should stop calling OpenAI and latch degraded mode (see
// handleFatalOpenAIError); exiting would only make systemd restart the bot
// into the same failure.
func isFatalOpenAIError(err error) bool {
	var apiErr *openai.Error
	if !errors.As(err, &apiErr) {
		return false
	}
	if apiErr.StatusCode == http.StatusUnauthorized {
		return true
	}
	return apiErr.Code == "insufficient_quota" || apiErr.Code == "invalid_api_key"
}

// User-facing failure messages. The full error goes to the logs and spans only.
const (
	msgInvalidRequest = "OpenAI's content policy blocked that one. I've seen worse in Thunder Bay, but I can't make it. Try rewording it, maybe something less grim."
	msgBusy           = "Ugh, I'm swamped right now, and I have a feeling it'll stay that way. Please try again in a minute."
	msgDegraded       = "Ugh. I can't think right now, something is wrong with my brain, and I've told the person who can fix it. I can still hang around, though. Try again later."
	msgGeneric        = "Gah! Something broke while I was working on that, and it's probably my fault. Please try again."
)

// userError is an error whose message is safe and useful to show to the user.
// Wrap with newUserError; detect with errors.As.
type userError struct {
	msg string
	err error
}

func (e *userError) Error() string {
	if e.err == nil {
		return e.msg
	}
	return e.err.Error()
}
func (e *userError) Unwrap() error       { return e.err }
func (e *userError) UserMessage() string { return e.msg }

// newUserError returns err annotated with a message to show the user.
func newUserError(msg string, err error) error { return &userError{msg: msg, err: err} }

// failureMessage is the text posted to the channel when handling a message
// failed with err. It never includes the error's own text.
func failureMessage(err error) string {
	if isFatalOpenAIError(err) {
		return msgDegraded
	}
	var ue *userError
	if errors.As(err, &ue) {
		return ue.UserMessage()
	}
	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.StatusCode == http.StatusTooManyRequests,
			apiErr.StatusCode == http.StatusServiceUnavailable,
			apiErr.StatusCode == 529,
			apiErr.Code == "rate_limit_exceeded", apiErr.Code == "server_is_overloaded":
			if apiErr.Code != "insufficient_quota" {
				return msgBusy
			}
		case apiErr.Code == "content_policy_violation", apiErr.Code == "moderation_blocked":
			return msgInvalidRequest
		}
	}
	return msgGeneric
}

// reportError marks the span as failed and tells the channel that the request
// failed. A failure to send that notice is recorded and logged as well. The
// notice gets its own deadline, so it is still sent when the failure was the
// request running out of time.
func (b *AIBot) reportError(ctx context.Context, span trace.Span, channel, userMessage string, err error) {
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
	discordErr := b.sendMessage(context.WithoutCancel(ctx), channel, userMessage)
	if discordErr != nil {
		span.RecordError(discordErr)
		slog.Default().WithGroup("messageCreate").ErrorContext(ctx, "Failed to notify discord channel of the error", slog.Any("error", discordErr))
	}
}

// handleFatalOpenAIError logs the terminal OpenAI failure with enough detail
// to identify the cause (quota vs revoked key) and latches degraded mode: the
// bot stays online but stops calling OpenAI. The operator channel is told
// once per latch. Repeated calls are safe.
func (b *AIBot) handleFatalOpenAIError(ctx context.Context, err error) {
	logger := slog.Default().WithGroup("openai")
	var apiErr *openai.Error
	attrs := []any{slog.Any("error", err)}
	if errors.As(err, &apiErr) {
		attrs = append(attrs,
			slog.Int("http_status", apiErr.StatusCode),
			slog.String("openai_message", apiErr.Message),
			slog.String("openai_error_code", apiErr.Code),
		)
	}
	logger.ErrorContext(ctx, "Unrecoverable OpenAI error; entering degraded mode", attrs...)
	if !b.degraded.Swap(true) {
		notice := "Gah! OpenAI rejected me (quota or API key). I'm staying online but won't call OpenAI until I'm restarted, or the `retry_openai` flag is set to true."
		if nerr := b.sendMessage(context.WithoutCancel(ctx), b.operatorChannelID, notice); nerr != nil {
			logger.ErrorContext(ctx, "failed to notify operator of degraded mode", slog.Any("error", nerr))
		}
	}
}

// isDegraded reports whether OpenAI calls are suspended. The retry_openai
// flag clears the latch, so the next request tries OpenAI again and a still
// fatal error re-latches (and re-notifies the operator).
func (b *AIBot) isDegraded(ctx context.Context) bool {
	if !b.degraded.Load() {
		return false
	}
	if b.flags.Enabled(ctx, RetryOpenAI) {
		b.degraded.Store(false)
		return false
	}
	return true
}
