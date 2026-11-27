package bot

import (
	"context"
	"strings"
	"testing"

	"openai-discord-bot/bot/storage"
)

func TestRecordHelpersFormats(t *testing.T) {
	tb := newTestBot(t)
	ctx := context.Background()
	m := mention("x")
	author := "alice (" + userID + ") on " + guildID

	if err := tb.bot.recordUser(ctx, "t1", m, "draw a cat"); err != nil {
		t.Fatal(err)
	}
	if err := tb.bot.recordEdit(ctx, "t1", m, "add a hat", "http://img/1"); err != nil {
		t.Fatal(err)
	}
	if err := tb.bot.recordBot(ctx, "t1", "meh"); err != nil {
		t.Fatal(err)
	}
	want := []storedMessage{
		{"t1", author, "User: draw a cat"},
		{"t1", author, "User: edit: add a hat (source: http://img/1)"},
		{"t1", "Bot", "meh"},
	}
	got := tb.threads.addedMessages()
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("record %d = %v, want %v", i, got[i], want[i])
		}
	}
}

// Old and new stored formats both read back as the bare request.
func TestStoredFormatsStripToRequest(t *testing.T) {
	for _, in := range []string{"add a hat", "edit: add a hat", "User: add a hat", "User: edit: add a hat"} {
		if got := stripRecordPrefix(sourceSuffix.ReplaceAllString(in+" (source: http://x/1)", "")); got != "add a hat" {
			t.Errorf("%q -> %q", in, got)
		}
	}
}

func TestCapChatContext(t *testing.T) {
	msg := func(s string) storage.ConversationMessage {
		return storage.ConversationMessage{Role: "user", Content: s}
	}
	thread := []storage.ConversationMessage{msg("aaaa"), msg("bbbb"), msg("cccc")}

	if got := capChatContext(thread, 100); len(got) != 3 {
		t.Errorf("under budget: %v", got)
	}
	got := capChatContext(thread, 9)
	if len(got) != 2 || got[0].Content != "bbbb" || got[1].Content != "cccc" {
		t.Errorf("newest kept in order: %v", got)
	}
	got = capChatContext([]storage.ConversationMessage{msg("old"), msg(strings.Repeat("é", 20))}, 5)
	if len(got) != 1 || got[0].Content != "ééééé" {
		t.Errorf("oversize newest cut: %v", got)
	}
	if got := capChatContext(nil, 5); len(got) != 0 {
		t.Errorf("empty: %v", got)
	}
}
