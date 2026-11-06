package bot

import (
	"context"
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
