package bot

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestHelpReplyIsStaticAndLinksHelpPage(t *testing.T) {
	tb := newTestBot(t)

	tb.bot.handleMessage(context.Background(), mention("help"))

	if len(tb.responses.calls) != 0 || len(tb.images.edited) != 0 {
		t.Fatal("help must not call a model")
	}
	sent := tb.discord.sentMessages()
	if len(sent) != 1 || sent[0].Content != helpText(testImageBaseURL) {
		t.Fatalf("unexpected Discord messages %+v", sent)
	}
	if !strings.Contains(sent[0].Content, testImageBaseURL+"/help/") {
		t.Errorf("help reply does not link the help page: %q", sent[0].Content)
	}
	if len(sent[0].Content) > 2000 {
		t.Errorf("help reply is %d chars, over Discord's limit", len(sent[0].Content))
	}
}

func TestHelpMeWriteStillChats(t *testing.T) {
	tb := newTestBot(t)
	tb.responses.fn = nil

	tb.bot.handleMessage(context.Background(), mention("help me write a poem"))

	if len(tb.responses.calls) != 1 {
		t.Fatalf("expected a chat completion, got %d calls", len(tb.responses.calls))
	}
}

func TestEditWithoutImageSaysSo(t *testing.T) {
	tb := newTestBot(t)

	tb.bot.handleMessage(context.Background(), mention("edit: add a hat"))

	if len(tb.responses.calls) != 0 || len(tb.images.edited) != 0 {
		t.Fatal("edit without an image must not call a model")
	}
	sent := tb.discord.sentMessages()
	if len(sent) != 1 || sent[0].Content != msgEditNeedsImage {
		t.Fatalf("unexpected Discord messages %+v", sent)
	}
}

// Every trigger and limit must appear in the help reply, the README and the
// help page. Add new triggers here (see AGENTS.md).
func TestDocsMentionEveryTrigger(t *testing.T) {
	needles := []string{"🧵", "🎨", "draw me a picture of", "✏️", "edit:", "redraw", "png", "jpeg", "webp", "50MB", "1024x1024", "help"}
	// The code must still recognise the triggers the docs promise.
	for _, c := range []struct {
		msg  string
		src  bool
		want messageIntent
	}{
		{"🎨 x", false, intentDraw}, {"draw me a picture of x", false, intentDraw},
		{"✏️ x", true, intentEdit}, {"edit: x", true, intentEdit}, {"redraw x", true, intentEdit},
		{"edit: x", false, intentEditNoImage}, {"help", false, intentHelp},
	} {
		if got, _ := classifyMessage(c.msg, botID, c.src); got != c.want {
			t.Errorf("classifyMessage(%q, image=%v) = %v, want %v", c.msg, c.src, got, c.want)
		}
	}
	readme, err := os.ReadFile("../README.md")
	if err != nil {
		t.Fatal(err)
	}
	page, err := os.ReadFile("../gallery/site/help.html")
	if err != nil {
		t.Fatal(err)
	}
	docs := map[string]string{
		"help reply": helpText(testImageBaseURL),
		"README":     string(readme),
		"help.html":  string(page),
	}
	for name, text := range docs {
		for _, n := range needles {
			if !strings.Contains(text, n) {
				t.Errorf("%s does not mention %q", name, n)
			}
		}
	}
}
