package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"openai-discord-bot/bot/flags"

	"github.com/bwmarrin/discordgo"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// The owner-only flag command, sent to the bot by DM (documented in the
// README's operator section only, not in the help reply):
//
//	flags                       list every flag
//	flag set <name> <value>     change one
//
// Only BOT_OWNER_USER_ID may use it. The same words from anyone else are
// ignored and logged. Anything else falls through to the normal handling.

// flagCommand is a parsed flag command.
type flagCommand struct {
	list bool
	// set fields; valid is false for a malformed command.
	name, value string
	valid       bool
}

// parseFlagCommand reads a DM as a flag command. isCmd is false when the
// message is not addressed to the command at all (it doesn't start with
// "flag" or "flags"); a command that starts right but is malformed returns
// isCmd true and cmd.valid false.
func parseFlagCommand(content, botID string) (cmd flagCommand, isCmd bool) {
	content = strings.NewReplacer("<@"+botID+">", " ", "<@!"+botID+">", " ").Replace(content)
	fields := strings.Fields(content)
	if len(fields) == 0 {
		return flagCommand{}, false
	}
	switch strings.ToLower(fields[0]) {
	case "flags":
		return flagCommand{list: true, valid: len(fields) == 1}, true
	case "flag":
		if len(fields) >= 4 && strings.ToLower(fields[1]) == "set" {
			return flagCommand{name: strings.ToLower(fields[2]), value: strings.Join(fields[3:], " "), valid: true}, true
		}
		return flagCommand{}, true
	}
	return flagCommand{}, false
}

// handleFlagDM handles a DM that may be a flag command. It returns true when
// the message was consumed (answered or deliberately ignored), false when
// normal handling should continue.
func (b *AIBot) handleFlagDM(ctx context.Context, m *discordgo.MessageCreate, botID string) bool {
	cmd, isCmd := parseFlagCommand(m.Content, botID)
	if !isCmd {
		return false
	}
	logger := slog.Default().WithGroup("flags")
	isOwner := b.ownerUserID != "" && m.Author.ID == b.ownerUserID
	if !isOwner {
		if !cmd.valid {
			// Not a well-formed command: let it be handled as it was before.
			return false
		}
		logger.WarnContext(ctx, "ignoring flag command from a user who is not the owner",
			slog.String("user", m.Author.ID), slog.Bool("owner_configured", b.ownerUserID != ""))
		return true
	}

	ctx, span := otel.GetTracerProvider().Tracer("AIBot").Start(ctx, "flagCommand")
	defer span.End()

	var reply string
	switch {
	case !cmd.valid:
		reply = flagUsage
	case cmd.list:
		reply = formatFlagList(b.flags.List())
	default:
		reply = b.setFlag(ctx, m.Author.ID, cmd.name, cmd.value)
	}
	if err := b.sendMessage(ctx, m.ChannelID, reply); err != nil {
		span.RecordError(err)
		logger.ErrorContext(ctx, "failed to reply to a flag command", slog.Any("error", err))
	}
	return true
}

// setFlag applies one change and returns the reply for the owner. A real
// change is logged, recorded on the span and announced in the operator
// channel; the store keeps the audit item.
func (b *AIBot) setFlag(ctx context.Context, by, name, value string) string {
	logger := slog.Default().WithGroup("flags")
	span := trace.SpanFromContext(ctx)
	span.SetAttributes(attribute.String("flag.name", name))

	c, err := b.flags.Set(ctx, name, value, by)
	switch {
	case errors.Is(err, flags.ErrUnknownFlag):
		return fmt.Sprintf("There is no flag called `%s`. Send `flags` for the list.", name)
	case errors.Is(err, flags.ErrInvalidValue):
		return fmt.Sprintf("`%s` can't be set to that: %v", name, err)
	case errors.Is(err, flags.ErrNoBackend):
		return "Flag storage isn't configured (BOT_FLAGS_TABLE), so flags can't be changed."
	case err != nil:
		span.RecordError(err)
		logger.ErrorContext(ctx, "failed to store a flag change", slog.String("flag", name), slog.Any("error", err))
		return fmt.Sprintf("Couldn't save `%s`; it is unchanged. Check the logs.", name)
	case c.Unchanged():
		return fmt.Sprintf("`%s` is already `%s`.", name, c.New)
	}

	span.SetAttributes(attribute.String("flag.old", c.Old), attribute.String("flag.new", c.New))
	logger.InfoContext(ctx, "feature flag changed",
		slog.String("flag", c.Name), slog.String("old", c.Old), slog.String("new", c.New), slog.String("by", c.By))
	notice := fmt.Sprintf("flag `%s`: %s → %s by the owner (%s)", c.Name, c.Old, c.New, c.By)
	if err := b.sendMessage(context.WithoutCancel(ctx), b.operatorChannelID, notice); err != nil {
		span.RecordError(err)
		logger.ErrorContext(ctx, "failed to announce a flag change", slog.Any("error", err))
	}
	return fmt.Sprintf("`%s`: %s → %s", c.Name, c.Old, c.New)
}

const flagUsage = "Usage: `flags` to list, `flag set <name> <value>` to change one."

// formatFlagList renders the owner's `flags` reply.
func formatFlagList(list []flags.Status) string {
	if len(list) == 0 {
		return "No flags are registered."
	}
	var sb strings.Builder
	for _, s := range list {
		fmt.Fprintf(&sb, "`%s` (%s) = `%s`, default `%s`", s.Name, s.Kind, s.Value, s.Default)
		if s.UpdatedBy != "" {
			fmt.Fprintf(&sb, ", set by %s at %s", s.UpdatedBy, s.UpdatedAt.UTC().Format(time.RFC3339))
		}
		if s.Description != "" {
			fmt.Fprintf(&sb, ": %s", s.Description)
		}
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}
