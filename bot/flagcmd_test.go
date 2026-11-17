package bot

import (
	"context"
	"encoding/base64"
	"errors"
	"sync"
	"testing"

	"openai-discord-bot/bot/flags"

	"github.com/bwmarrin/discordgo"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
)

const (
	testOwnerID = "owner-1"
	dmChannelID = "dm-1"
)

type setCall struct{ name, value, by string }

// fakeFlags is an in-memory flagStore.
type fakeFlags struct {
	mu      sync.Mutex
	bools   map[string]bool
	queried []string
	sets    []setCall
	lists   int
	setErr  error
	// unchanged makes Set report that the value was already set.
	unchanged bool
}

func newFakeFlags() *fakeFlags { return &fakeFlags{bools: map[string]bool{}} }

func (f *fakeFlags) Enabled(_ context.Context, fl flags.BoolFlag) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queried = append(f.queried, fl.Name())
	if v, ok := f.bools[fl.Name()]; ok {
		return v
	}
	return fl.Default()
}

func (f *fakeFlags) Set(_ context.Context, name, value, by string) (flags.Change, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sets = append(f.sets, setCall{name, value, by})
	if f.setErr != nil {
		return flags.Change{}, f.setErr
	}
	if f.unchanged {
		return flags.Change{Name: name, Old: "true", New: "true", By: by}, nil
	}
	return flags.Change{Name: name, Old: "false", New: "true", By: by}, nil
}

func (f *fakeFlags) List() []flags.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lists++
	return []flags.Status{{Definition: flags.Definition{Name: "edit_thread_context", Default: "false"}, Value: "false"}}
}

func (f *fakeFlags) setCalls() []setCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]setCall(nil), f.sets...)
}

func newFlagTestBot(t *testing.T) (*testBot, *fakeFlags) {
	t.Helper()
	tb := newTestBot(t)
	ff := newFakeFlags()
	tb.bot.flags = ff
	tb.bot.ownerUserID = testOwnerID
	tb.responses.fn = func(context.Context, responses.ResponseNewParams) (*responses.Response, error) {
		return textResponse("chat"), nil
	}
	return tb, ff
}

// dm builds a direct message (no guild, no mention) from author.
func dm(author, content string) *discordgo.MessageCreate {
	return &discordgo.MessageCreate{Message: &discordgo.Message{
		ID:        "dm-msg-1",
		ChannelID: dmChannelID,
		Content:   content,
		Author:    &discordgo.User{ID: author, Username: "someone"},
	}}
}

func sentTo(msgs []sentMessage, channel string) int {
	n := 0
	for _, m := range msgs {
		if m.Channel == channel {
			n++
		}
	}
	return n
}

func TestOwnerDMListsFlags(t *testing.T) {
	for _, content := range []string{"flags", "  FLAGS ", "<@" + botID + "> flags"} {
		tb, ff := newFlagTestBot(t)
		tb.bot.handleMessage(context.Background(), dm(testOwnerID, content))
		sent := tb.discord.sentMessages()
		if ff.lists != 1 || len(sent) != 1 || sent[0].Channel != dmChannelID {
			t.Fatalf("%q: lists=%d sent=%+v, want one reply in the DM", content, ff.lists, sent)
		}
		if tb.responses.callCount() != 0 {
			t.Fatalf("%q: the flag command must not call the model", content)
		}
	}
}

func TestOwnerDMSetsFlagAndNotifiesOperator(t *testing.T) {
	tb, ff := newFlagTestBot(t)
	tb.bot.handleMessage(context.Background(), dm(testOwnerID, "flag set edit_thread_context on"))

	if got := ff.setCalls(); len(got) != 1 || got[0] != (setCall{"edit_thread_context", "on", testOwnerID}) {
		t.Fatalf("Set calls = %+v", got)
	}
	sent := tb.discord.sentMessages()
	if sentTo(sent, dmChannelID) != 1 || sentTo(sent, testOperatorChannel) != 1 || len(sent) != 2 {
		t.Fatalf("want one DM confirmation and one operator notice, got %+v", sent)
	}
	if tb.responses.callCount() != 0 || len(tb.threads.addedMessages()) != 0 {
		t.Fatal("the flag command must not call the model or record thread history")
	}
}

func TestOwnerDMSetKeepsMultiWordValue(t *testing.T) {
	tb, ff := newFlagTestBot(t)
	tb.bot.handleMessage(context.Background(), dm(testOwnerID, "flag set some_mode very  fancy"))
	if got := ff.setCalls(); len(got) != 1 || got[0].value != "very fancy" {
		t.Fatalf("Set calls = %+v", got)
	}
}

func TestOwnerDMSetUnchangedDoesNotNotifyOperator(t *testing.T) {
	tb, ff := newFlagTestBot(t)
	ff.unchanged = true
	tb.bot.handleMessage(context.Background(), dm(testOwnerID, "flag set edit_thread_context on"))
	sent := tb.discord.sentMessages()
	if sentTo(sent, dmChannelID) != 1 || sentTo(sent, testOperatorChannel) != 0 {
		t.Fatalf("an unchanged flag gets a DM reply only, got %+v", sent)
	}
}

func TestOwnerDMSetErrorRepliesWithoutNotice(t *testing.T) {
	for _, err := range []error{flags.ErrUnknownFlag, flags.ErrInvalidValue, flags.ErrNoBackend, errors.New("dynamodb down")} {
		tb, ff := newFlagTestBot(t)
		ff.setErr = err
		tb.bot.handleMessage(context.Background(), dm(testOwnerID, "flag set edit_thread_context on"))
		sent := tb.discord.sentMessages()
		if sentTo(sent, dmChannelID) != 1 || sentTo(sent, testOperatorChannel) != 0 {
			t.Fatalf("%v: want one DM reply and no notice, got %+v", err, sent)
		}
	}
}

func TestOwnerDMMalformedCommandRepliesWithoutWriting(t *testing.T) {
	for _, content := range []string{"flag set", "flag set edit_thread_context", "flag", "flag frobnicate x y"} {
		tb, ff := newFlagTestBot(t)
		tb.bot.handleMessage(context.Background(), dm(testOwnerID, content))
		if len(ff.setCalls()) != 0 {
			t.Fatalf("%q: a malformed command must not write", content)
		}
		sent := tb.discord.sentMessages()
		if len(sent) != 1 || sent[0].Channel != dmChannelID {
			t.Fatalf("%q: want one usage reply in the DM, got %+v", content, sent)
		}
	}
}

func TestNonOwnerFlagCommandIsIgnored(t *testing.T) {
	for _, content := range []string{"flags", "flag set edit_thread_context on", "<@" + botID + "> flags"} {
		tb, ff := newFlagTestBot(t)
		m := dm("intruder", content)
		m.Mentions = []*discordgo.User{{ID: botID}}
		tb.bot.handleMessage(context.Background(), m)
		if len(ff.setCalls()) != 0 || ff.lists != 0 || len(tb.discord.sentMessages()) != 0 || tb.responses.callCount() != 0 {
			t.Fatalf("%q from a non-owner must be ignored silently", content)
		}
	}
}

func TestFlagCommandDisabledWithoutOwner(t *testing.T) {
	tb, ff := newFlagTestBot(t)
	tb.bot.ownerUserID = ""
	m := dm("", "flag set edit_thread_context on")
	tb.bot.handleMessage(context.Background(), m)
	if len(ff.setCalls()) != 0 || len(tb.discord.sentMessages()) != 0 {
		t.Fatal("with no owner configured the command must do nothing, even for an empty author ID")
	}
}

func TestFlagCommandInGuildIsOrdinaryChat(t *testing.T) {
	tb, ff := newFlagTestBot(t)
	m := mention("flag set edit_thread_context on")
	m.Author = &discordgo.User{ID: testOwnerID, Username: "owner"}
	tb.bot.handleMessage(context.Background(), m)
	if len(ff.setCalls()) != 0 {
		t.Fatal("flag commands only work in DMs")
	}
	if tb.responses.callCount() != 1 {
		t.Fatal("in a guild the message must be handled as chat, as before")
	}
}

func TestOtherDMsBehaveAsBefore(t *testing.T) {
	// No mention: ignored, as before.
	tb, _ := newFlagTestBot(t)
	tb.bot.handleMessage(context.Background(), dm(testOwnerID, "hello"))
	if len(tb.discord.sentMessages()) != 0 || tb.responses.callCount() != 0 {
		t.Fatal("a DM without a mention or command must be ignored")
	}

	// A DM that mentions the bot and is not a command: chat, as before.
	tb, ff := newFlagTestBot(t)
	m := dm("someone", "<@"+botID+"> flags are pretty")
	m.Mentions = []*discordgo.User{{ID: botID}}
	tb.bot.handleMessage(context.Background(), m)
	if tb.responses.callCount() != 1 || ff.lists != 0 {
		t.Fatal("a mention that is not exactly a command must chat")
	}
}

func TestEditChecksThreadContextFlagWithoutChangingTheRequest(t *testing.T) {
	prompts := map[bool]string{}
	for _, on := range []bool{false, true} {
		tb, ff := newFlagTestBot(t)
		ff.bools[EditThreadContext.Name()] = on
		const srcURL = "https://cdn.example/source.png"
		tb.imgStore.sources[srcURL] = pngBytes
		tb.images.edit = func(context.Context, openai.ImageEditParams) (*openai.ImagesResponse, error) {
			return imageResponse(base64.StdEncoding.EncodeToString(pngBytes)), nil
		}
		m := mention("edit: add a hat")
		m.Attachments = []*discordgo.MessageAttachment{{URL: srcURL, Filename: "source.png"}}
		tb.bot.handleMessage(context.Background(), m)

		if len(tb.images.edited) != 1 {
			t.Fatalf("flag=%v: expected one Edit call, got %d", on, len(tb.images.edited))
		}
		prompts[on] = tb.images.edited[0].Prompt
		found := false
		for _, q := range ff.queried {
			found = found || q == EditThreadContext.Name()
		}
		if !found {
			t.Fatalf("flag=%v: the edit path must check %s", on, EditThreadContext.Name())
		}
	}
	if prompts[false] != prompts[true] {
		t.Fatalf("until the feature exists the flag must not change the request: %q vs %q", prompts[false], prompts[true])
	}
}

func TestEditThreadContextIsRegisteredOffByDefault(t *testing.T) {
	def, ok := flagRegistry.Lookup("edit_thread_context")
	if !ok || def.Kind != flags.KindBool || EditThreadContext.Default() {
		t.Fatalf("edit_thread_context = %+v (registered=%v), want a bool defaulting to off", def, ok)
	}
	if NewFlags(nil).Enabled(context.Background(), EditThreadContext) {
		t.Fatal("without storage the flag must be off")
	}
}
