package bot

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
)

func cleanDrawBot(t *testing.T) *testBot {
	t.Helper()
	tb := newTestBot(t)
	tb.images.generate = func(context.Context, openai.ImageGenerateParams) (*openai.ImagesResponse, error) {
		return imageResponse(base64.StdEncoding.EncodeToString(pngBytes)), nil
	}
	return tb
}

func TestCleanDrawSkipsDanbotPrompt(t *testing.T) {
	tb := cleanDrawBot(t)
	tb.bot.handleMessage(context.Background(), mention("🎨 "+cleanEmoji+" a cat"))

	if len(tb.images.generated) != 1 {
		t.Fatalf("expected one Generate call, got %d", len(tb.images.generated))
	}
	req := tb.images.generated[0]
	if req.Prompt != "a cat" {
		t.Errorf("clean prompt should be the user's text alone, got %q", req.Prompt)
	}
	if req.SafetyIdentifier.Value != userID || req.Size != openai.ImageGenerateParamsSize1024x1024 {
		t.Errorf("safety identifier and size must still be set: %+v", req)
	}
	sent := tb.discord.sentMessages()
	if len(sent) != 1 || !hasCleanEmoji(sent[0].Content) {
		t.Errorf("the reply should carry the clean marker, got %+v", sent)
	}
	if len(tb.imgStore.storedImages()) != 1 {
		t.Error("clean image should be archived like any other")
	}
}

func TestDefaultDrawStillUsesDanbotPromptAndIsNotMarked(t *testing.T) {
	tb := cleanDrawBot(t)
	tb.bot.handleMessage(context.Background(), mention("🎨 a cat"))

	if p := tb.images.generated[0].Prompt; !strings.Contains(p, testDrawing) {
		t.Errorf("default draw lost the style block: %q", p)
	}
	if sent := tb.discord.sentMessages(); len(sent) != 1 || hasCleanEmoji(sent[0].Content) {
		t.Errorf("a default drawing must not be marked clean: %+v", sent)
	}
}

func TestCleanEmojiOnlyCountsWithDrawOrEdit(t *testing.T) {
	tb := newTestBot(t)
	tb.responses.fn = nil
	tb.bot.handleMessage(context.Background(), mention(cleanEmoji+" hello"))
	if len(tb.images.generated) != 0 || len(tb.images.edited) != 0 || len(tb.responses.calls) != 1 {
		t.Fatal("a lone clean emoji should leave chat as chat")
	}
}

func TestCleanEditSkipsDanbotPrompt(t *testing.T) {
	tb := editThreadBot(t, false, nil)
	tb.bot.handleMessage(context.Background(), editMention("edit: "+cleanEmoji+" add a hat"))

	if len(tb.images.edited) != 1 {
		t.Fatalf("expected one Edit call, got %d", len(tb.images.edited))
	}
	if p := tb.images.edited[0].Prompt; p != "add a hat" {
		t.Errorf("clean edit prompt should be the change alone, got %q", p)
	}
	if sent := tb.discord.sentMessages(); len(sent) != 1 || !hasCleanEmoji(sent[0].Content) {
		t.Errorf("the edit reply should carry the clean marker, got %+v", sent)
	}
}

func TestDefaultEditUnchangedAndUnmarked(t *testing.T) {
	tb := editThreadBot(t, false, nil)
	tb.bot.handleMessage(context.Background(), editMention("edit: add a hat"))

	if p := tb.images.edited[0].Prompt; !strings.Contains(p, testEditKeep) {
		t.Errorf("default edit lost its keep list: %q", p)
	}
	if sent := tb.discord.sentMessages(); len(sent) != 1 || hasCleanEmoji(sent[0].Content) {
		t.Errorf("a default edit must not be marked clean: %+v", sent)
	}
}

func TestCleanIsStickyAcrossEdits(t *testing.T) {
	tb := editThreadBot(t, false, nil)
	m := mention("edit: add a hat") // no 🫧 on this request
	m.ReferencedMessage = &discordgo.Message{
		Author:      &discordgo.User{ID: tb.discord.BotUser().ID},
		Content:     cleanReply("here ya go", true),
		Attachments: []*discordgo.MessageAttachment{{URL: editSrcURL, Filename: "source.png"}},
	}
	tb.bot.handleMessage(context.Background(), m)

	if p := tb.images.edited[0].Prompt; p != "add a hat" {
		t.Errorf("an edit of a clean image should stay clean, got %q", p)
	}
	if sent := tb.discord.sentMessages(); len(sent) != 1 || !hasCleanEmoji(sent[0].Content) {
		t.Errorf("the marker should carry to the next image, got %+v", sent)
	}
}

func TestCleanEditStillUsesThreadContext(t *testing.T) {
	tb := editThreadBot(t, true, sampleThread)
	tb.responses.fn = func(context.Context, responses.ResponseNewParams) (*responses.Response, error) {
		return textResponse("- everyone wears a hat"), nil
	}
	tb.bot.handleMessage(context.Background(), editMention("edit: "+cleanEmoji+" add a dog"))

	p := tb.images.edited[0].Prompt
	if !strings.HasPrefix(p, "add a dog") || !strings.Contains(p, "everyone wears a hat") {
		t.Errorf("clean edit should keep thread directives: %q", p)
	}
	if strings.Contains(p, testEditKeep) {
		t.Errorf("clean edit must not carry the keep list: %q", p)
	}
}

func TestStripCleanEmoji(t *testing.T) {
	for in, want := range map[string]string{
		"a " + cleanEmoji + " cat": "a cat",
		cleanEmoji + " a cat":      "a cat",
		"a cat " + cleanEmoji:      "a cat",
		"a  cat":                   "a  cat",
	} {
		if got := stripCleanEmoji(in); got != want {
			t.Errorf("stripCleanEmoji(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCleanEditPromptLengthFallback(t *testing.T) {
	change := "add a hat"
	short := []string{"one", "two"}
	if got := buildCleanEditPromptWithContext(change, short); !strings.HasPrefix(got, change) || !strings.Contains(got, "two") {
		t.Errorf("short directives should all fit: %q", got)
	}
	// The second directive would take the prompt past the limit, so only the
	// first is kept.
	long := []string{"one", strings.Repeat("x", maxEditPromptChars)}
	got := buildCleanEditPromptWithContext(change, long)
	if !strings.Contains(got, "one") || strings.Contains(got, "xxx") {
		t.Errorf("over-long directive should be dropped: %.80q", got)
	}
	// Nothing fits: the plain change.
	if got := buildCleanEditPromptWithContext(change, []string{strings.Repeat("x", maxEditPromptChars)}); got != change {
		t.Errorf("want the plain change, got %.80q", got)
	}
	if got := buildCleanEditPromptWithContext(change, nil); got != change {
		t.Errorf("no directives should give the change, got %q", got)
	}
}

func TestEditStaysDefaultWhenNotCleanSource(t *testing.T) {
	for name, author := range map[string]string{"unmarked bot drawing": "", "marked message from a user": "user-2"} {
		t.Run(name, func(t *testing.T) {
			tb := editThreadBot(t, false, nil)
			content := "here ya go"
			if author != "" {
				content = cleanEmoji + " look"
			}
			if author == "" {
				author = tb.discord.BotUser().ID
			}
			m := mention("edit: add a hat")
			m.ReferencedMessage = &discordgo.Message{
				Author:      &discordgo.User{ID: author},
				Content:     content,
				Attachments: []*discordgo.MessageAttachment{{URL: editSrcURL, Filename: "source.png"}},
			}
			if author != tb.discord.BotUser().ID {
				// Not the bot's message: the image comes from an attachment.
				m.ReferencedMessage = nil
				m.Attachments = []*discordgo.MessageAttachment{{URL: editSrcURL, Filename: "source.png"}}
				m.ReferencedMessage = &discordgo.Message{Author: &discordgo.User{ID: author}, Content: content}
			}
			tb.bot.handleMessage(context.Background(), m)
			if len(tb.images.edited) != 1 {
				t.Fatalf("expected one Edit call, got %d", len(tb.images.edited))
			}
			if p := tb.images.edited[0].Prompt; !strings.Contains(p, testEditKeep) {
				t.Errorf("edit should stay default, got %q", p)
			}
			if sent := tb.discord.sentMessages(); len(sent) != 1 || hasCleanEmoji(sent[0].Content) {
				t.Errorf("reply must not be marked clean: %+v", sent)
			}
		})
	}
}
