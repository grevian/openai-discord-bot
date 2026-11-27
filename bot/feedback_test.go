package bot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"openai-discord-bot/bot/feedback"

	"github.com/bwmarrin/discordgo"
)

func withFeedback(t *testing.T) (*testBot, *fakeFeedbackStore) {
	t.Helper()
	tb := newTestBot(t)
	fs := &fakeFeedbackStore{}
	tb.bot.feedback = fs
	return tb, fs
}

func TestClassifyFeedback(t *testing.T) {
	tests := []struct {
		in         string
		wantIntent messageIntent
		wantText   string
		image      bool
	}{
		{"<@bot-1> feedback: it is slow", intentFeedback, "it is slow", false},
		{"<@bot-1> Feedback x", intentFeedback, "x", false},
		{"<@!bot-1> feedback x", intentFeedback, "x", false},
		{"<@bot-1> idea - more cats", intentIdea, "more cats", false},
		{"<@bot-1> idea:x", intentIdea, "x", false},
		{"<@bot-1> feedback", intentFeedback, "", false},
		{"<@bot-1> feedback 🎨 drawing is broken", intentFeedback, "🎨 drawing is broken", false},
		{"<@bot-1> feedback edit: broken", intentFeedback, "edit: broken", true},
		{"<@bot-1> feedback https://example.com/a", intentFeedback, "https://example.com/a", false},
		{"<@bot-1> feedbacks are nice", intentChat, "", false},
		{"<@bot-1> ideas please", intentChat, "", false},
		{"<@bot-1> idea's a good one", intentChat, "", false},
		{"<@bot-1> my feedback on this poem", intentChat, "", false},
		{"<@bot-1> what's your idea", intentChat, "", false},
		{"<@bot-1> help", intentHelp, "", false},
	}
	for _, tc := range tests {
		got, text := classifyMessage(tc.in, "bot-1", tc.image)
		if got != tc.wantIntent {
			t.Errorf("%q: intent %v, want %v", tc.in, got, tc.wantIntent)
			continue
		}
		if (tc.wantIntent == intentFeedback || tc.wantIntent == intentIdea) && text != tc.wantText {
			t.Errorf("%q: text %q, want %q", tc.in, text, tc.wantText)
		}
	}
}

func TestFeedbackIsStoredAckedAndReported(t *testing.T) {
	tb, fs := withFeedback(t)
	tb.bot.handleMessage(context.Background(), mention("feedback: the drawings are too grim"))

	items := fs.stored()
	if len(items) != 1 {
		t.Fatalf("want 1 item, got %+v", items)
	}
	it := items[0]
	if it.Kind != "feedback" || it.Text != "the drawings are too grim" || it.UserID != userID || it.Username != "alice" ||
		it.GuildID != guildID || it.ChannelID != channelID || it.MessageID != "msg-1" || it.ReplyToMessageID != "" {
		t.Errorf("unexpected item %+v", it)
	}
	if !strings.HasSuffix(it.SortKey(), "#msg-1") {
		t.Errorf("sort key %q", it.SortKey())
	}
	if tb.responses.callCount() != 0 || len(tb.images.generated) != 0 {
		t.Error("feedback must not call a model")
	}
	if len(tb.threads.addedMessages()) != 0 {
		t.Error("feedback must not be stored as conversation")
	}
	sent := tb.discord.sentMessages()
	if len(sent) != 2 || sent[0].Channel != channelID || sent[0].Content != msgFeedbackThanks {
		t.Fatalf("unexpected messages %+v", sent)
	}
	notice := sent[1]
	if notice.Channel != testOperatorChannel || notice.AllowedMentions == nil ||
		len(notice.AllowedMentions.Parse) != 0 || len(notice.AllowedMentions.Users) != 0 || notice.AllowedMentions.RepliedUser {
		t.Errorf("operator notice must be sent with empty AllowedMentions: %+v", notice)
	}
	if !strings.Contains(notice.Content, "the drawings are too grim") || !strings.Contains(notice.Content, it.SortKey()) ||
		!strings.Contains(notice.Content, "https://discord.com/channels/guild-1/chan-1/msg-1") {
		t.Errorf("notice content %q", notice.Content)
	}
}

func TestIdeaKind(t *testing.T) {
	tb, fs := withFeedback(t)
	tb.bot.handleMessage(context.Background(), mention("idea more cats"))
	items := fs.stored()
	if len(items) != 1 || items[0].Kind != "idea" {
		t.Fatalf("got %+v", items)
	}
	if got := tb.discord.sentMessages()[0].Content; got != msgIdeaThanks {
		t.Errorf("ack %q", got)
	}
}

func TestFeedbackReplyToBotMessageRecordsIt(t *testing.T) {
	tb, fs := withFeedback(t)
	long := strings.Repeat("é", 600)
	tb.discord.messages["bot-msg"] = &discordgo.Message{ID: "bot-msg", Author: &discordgo.User{ID: botID}, Content: long}
	m := mention("feedback nice one")
	m.MessageReference = &discordgo.MessageReference{MessageID: "bot-msg", ChannelID: channelID}
	tb.bot.handleMessage(context.Background(), m)
	it := fs.stored()[0]
	if it.ReplyToMessageID != "bot-msg" || len([]rune(it.ReplyToContent)) != 500 {
		t.Errorf("reply fields wrong: %q len %d", it.ReplyToMessageID, len([]rune(it.ReplyToContent)))
	}
}

func TestFeedbackReplyToUserMessageRecordsNothing(t *testing.T) {
	tb, fs := withFeedback(t)
	m := mention("feedback nice one")
	m.ReferencedMessage = &discordgo.Message{ID: "u-msg", Author: &discordgo.User{ID: "someone"}, Content: "hi"}
	tb.bot.handleMessage(context.Background(), m)
	it := fs.stored()[0]
	if it.ReplyToMessageID != "" || it.ReplyToContent != "" {
		t.Errorf("unexpected reply fields %+v", it)
	}
}

func TestFeedbackNotConfigured(t *testing.T) {
	tb := newTestBot(t)
	tb.bot.handleMessage(context.Background(), mention("feedback hello"))
	sent := tb.discord.sentMessages()
	if len(sent) != 1 || sent[0].Content != msgFeedbackOff {
		t.Fatalf("unexpected messages %+v", sent)
	}
}

func TestTypedNilFeedbackStoreIsNotConfigured(t *testing.T) {
	// NewAIBot must leave the interface nil for a nil *feedback.Store.
	var opts Options
	opts.Feedback = (*feedback.Store)(nil)
	b := &AIBot{}
	if opts.Feedback != nil {
		b.feedback = opts.Feedback
	}
	if b.feedback != nil {
		t.Fatal("typed nil leaked into the interface")
	}
}

func TestBareFeedbackGivesUsage(t *testing.T) {
	tb, fs := withFeedback(t)
	tb.bot.handleMessage(context.Background(), mention("feedback"))
	sent := tb.discord.sentMessages()
	if len(sent) != 1 || sent[0].Content != msgFeedbackUsage || len(fs.stored()) != 0 {
		t.Fatalf("unexpected %+v / %+v", sent, fs.stored())
	}
}

func TestFeedbackRateLimitPerUser(t *testing.T) {
	tb, fs := withFeedback(t)
	for i := 0; i < 6; i++ {
		m := mention(fmt.Sprintf("feedback n%d", i))
		m.ID = fmt.Sprintf("m%d", i)
		tb.bot.handleMessage(context.Background(), m)
	}
	if len(fs.stored()) != 5 {
		t.Fatalf("want 5 stored, got %d", len(fs.stored()))
	}
	sent := tb.discord.sentMessages()
	if last := sent[len(sent)-1]; last.Content != msgFeedbackSlowDown || last.Channel != channelID {
		t.Errorf("last message %+v", last)
	}
	other := mention("feedback from bob")
	other.Author = &discordgo.User{ID: "user-2", Username: "bob"}
	other.ID = "m-bob"
	tb.bot.handleMessage(context.Background(), other)
	if len(fs.stored()) != 6 {
		t.Error("another user must be unaffected")
	}
}

func TestFeedbackStoreErrorReportsFailure(t *testing.T) {
	tb, fs := withFeedback(t)
	fs.err = errors.New("dynamo down")
	tb.bot.handleMessage(context.Background(), mention("feedback hi"))
	sent := tb.discord.sentMessages()
	if len(sent) != 1 || sent[0].Content != msgFeedbackFailed || sent[0].Channel != channelID {
		t.Fatalf("unexpected messages %+v", sent)
	}
}

func TestFeedbackWorksWhileDegraded(t *testing.T) {
	tb, fs := withFeedback(t)
	tb.bot.degraded.Store(true)
	tb.bot.handleMessage(context.Background(), mention("idea a hat"))
	if len(fs.stored()) != 1 || tb.discord.sentMessages()[0].Content != msgIdeaThanks {
		t.Errorf("degraded mode blocked feedback: %+v", tb.discord.sentMessages())
	}
}

func TestLongFeedbackIsTruncated(t *testing.T) {
	tb, fs := withFeedback(t)
	tb.bot.handleMessage(context.Background(), mention("feedback "+strings.Repeat("x", 2000)))
	it := fs.stored()[0]
	if len([]rune(it.Text)) != 1500 || !it.Truncated {
		t.Errorf("len %d truncated %v", len([]rune(it.Text)), it.Truncated)
	}
}

func TestHelpMentionsFeedback(t *testing.T) {
	h := helpText(testImageBaseURL)
	if !strings.Contains(h, "feedback") || len(h) > 2000 {
		t.Errorf("help reply (%d chars) must mention feedback", len(h))
	}
}
