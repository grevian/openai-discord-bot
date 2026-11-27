package bot

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/bwmarrin/discordgo"
)

func userWasMentioned(user *discordgo.User, mentioned []*discordgo.User) bool {
	if user == nil {
		return false
	}

	for u := range mentioned {
		if user.ID == mentioned[u].ID {
			return true
		}
	}

	return false
}

// messageIntent is what a mention is asking the bot to do.
type messageIntent int

const (
	intentChat messageIntent = iota
	intentDraw
	intentEdit
	intentEditNoImage // edit trigger without an image to edit
	intentHelp
	// intentSummarize asks for a summary of a linked page; the prompt is
	// the URL.
	intentSummarize
)

// editAtStart matches a message that opens with an edit trigger.
var editAtStart = regexp.MustCompile(`^(✏️|edit:|redraw\b)`)

// classifyMessage decides what a message is asking for and returns the prompt
// to send for it. botID is stripped from the prompt; hasSourceImage reports
// whether an image to edit is available.
func classifyMessage(content, botID string, hasSourceImage bool) (messageIntent, string) {
	// Strip our UserId out of messages to keep the record from being too confusing,
	sanitizedUserPrompt := strings.ReplaceAll(content, fmt.Sprintf("<@%s>", botID), "")
	lowerPrompt := strings.ToLower(sanitizedUserPrompt)

	editTrigger := strings.Contains(sanitizedUserPrompt, "✏️") ||
		strings.Contains(lowerPrompt, "edit:") ||
		strings.Contains(lowerPrompt, "redraw")
	wantsEdit := hasSourceImage && editTrigger
	wantsDraw := strings.Contains(lowerPrompt, "🎨") || strings.Contains(lowerPrompt, "draw me a picture of")
	summaryURL, wantsSummary := summarizeTarget(strings.ReplaceAll(sanitizedUserPrompt, fmt.Sprintf("<@!%s>", botID), ""))

	// Without an image the match is anchored to the start of the message, so
	// ordinary chat ("credit: ...", "can you redraw my thinking") still chats.
	wantsEditNoImage := !hasSourceImage && editAtStart.MatchString(strings.TrimSpace(lowerPrompt))

	switch {
	case isHelpRequest(sanitizedUserPrompt):
		return intentHelp, sanitizedUserPrompt
	case wantsEditNoImage && !wantsDraw:
		return intentEditNoImage, sanitizedUserPrompt
	case wantsEdit:
		return intentEdit, stripCleanEmoji(stripEditTriggers(sanitizedUserPrompt))
	case wantsDraw:
		// Strip the prompt prefix out of the message
		return intentDraw, stripCleanEmoji(stripDrawTriggers(sanitizedUserPrompt))
	case wantsSummary:
		return intentSummarize, summaryURL
	default:
		return intentChat, sanitizedUserPrompt
	}
}

// String is the intent's name as recorded in the `intent` span attribute.
func (i messageIntent) String() string {
	switch i {
	case intentDraw:
		return "draw"
	case intentEdit:
		return "edit"
	case intentEditNoImage:
		return "edit_no_image"
	case intentHelp:
		return "help"
	case intentSummarize:
		return "summarize"
	default:
		return "chat"
	}
}

// stripDrawTriggers removes the draw triggers from a prompt, keeping the
// user's original case.
func stripDrawTriggers(prompt string) string {
	out := strings.ReplaceAll(prompt, "🎨", "")
	out = drawPhrase.ReplaceAllString(out, "")
	return strings.TrimSpace(out)
}

var drawPhrase = regexp.MustCompile(`(?i)draw me a picture of`)

// editPhrase matches the text edit triggers in any casing; "redraw" only as a
// whole word, so "redrawn" is left alone.
var editPhrase = regexp.MustCompile(`(?i)\bedit:|\bredraw\b`)

func stripEditTriggers(prompt string) string {
	out := strings.ReplaceAll(prompt, "✏️", "")
	out = editPhrase.ReplaceAllString(out, "")
	return strings.TrimSpace(out)
}
