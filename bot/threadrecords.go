package bot

import (
	"context"
	"fmt"

	"github.com/bwmarrin/discordgo"

	"openai-discord-bot/bot/storage"
)

// maxChatContextChars caps the stored thread text sent with a chat request.
// The newest messages are kept.
const maxChatContextChars = 12000

// userRecordLabel starts every stored user message. The readers in
// editcontext.go also accept older records (a bare draw prompt, "edit: ...").
const userRecordLabel = "User: "

// botRecordAuthor is the author stored with the bot's own messages.
const botRecordAuthor = "Bot"

// recordAuthor is the author label stored with a user's message.
func recordAuthor(m *discordgo.MessageCreate) string {
	return fmt.Sprintf("%s (%s) on %s", m.Author.Username, m.Author.ID, m.GuildID)
}

// recordUser stores a user's message in the thread.
func (b *AIBot) recordUser(ctx context.Context, thread string, m *discordgo.MessageCreate, text string) error {
	return b.addThreadMessage(ctx, thread, recordAuthor(m), userRecordLabel+text)
}

// recordEdit stores an image edit request with the image it applies to.
func (b *AIBot) recordEdit(ctx context.Context, thread string, m *discordgo.MessageCreate, prompt, sourceURL string) error {
	return b.recordUser(ctx, thread, m, fmt.Sprintf("edit: %s (source: %s)", prompt, sourceURL))
}

// recordBot stores the bot's reply (text or an image URL) in the thread.
func (b *AIBot) recordBot(ctx context.Context, thread, text string) error {
	return b.addThreadMessage(ctx, thread, botRecordAuthor, text)
}

// capChatContext keeps the newest messages whose total length fits maxChars
// runes, in their original order. The newest message is always kept, cut to
// the budget if it alone is longer.
func capChatContext(thread []storage.ConversationMessage, maxChars int) []storage.ConversationMessage {
	used := 0
	start := len(thread)
	for i := len(thread) - 1; i >= 0; i-- {
		n := len([]rune(thread[i].Content))
		if used+n > maxChars {
			break
		}
		used += n
		start = i
	}
	if start == len(thread) && len(thread) > 0 {
		last := thread[len(thread)-1]
		last.Content, _ = truncateRunes(last.Content, maxChars)
		return []storage.ConversationMessage{last}
	}
	return thread[start:]
}
