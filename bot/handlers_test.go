package bot

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"openai-discord-bot/bot/storage"

	"github.com/bwmarrin/discordgo"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
)

func TestHandleMessageIgnoresOwnMessages(t *testing.T) {
	tb := newTestBot(t)
	m := mention("hello")
	m.Author = &discordgo.User{ID: botID}
	tb.bot.handleMessage(context.Background(), m)
	if tb.responses.callCount() != 0 || len(tb.discord.sentMessages()) != 0 {
		t.Fatal("the bot must not answer its own messages")
	}
}

func TestHandleMessageIgnoresMessagesWithoutMention(t *testing.T) {
	tb := newTestBot(t)
	m := mention("hello")
	m.Mentions = nil
	tb.bot.handleMessage(context.Background(), m)
	if tb.responses.callCount() != 0 || len(tb.discord.sentMessages()) != 0 {
		t.Fatal("the bot must only answer mentions")
	}
}

func TestMessageCreateDelegates(t *testing.T) {
	tb := newTestBot(t)
	tb.responses.fn = func(context.Context, responses.ResponseNewParams) (*responses.Response, error) {
		return textResponse("hi"), nil
	}
	tb.bot.messageCreate(nil, mention("hello"))
	if got := tb.discord.sentMessages(); len(got) != 1 || got[0].Content != "hi" {
		t.Fatalf("unexpected messages %+v", got)
	}
}

func TestChatCompletion(t *testing.T) {
	tb := newTestBot(t)
	tb.responses.fn = func(context.Context, responses.ResponseNewParams) (*responses.Response, error) {
		return textResponse("general kenobi"), nil
	}

	tb.bot.handleMessage(context.Background(), mention("hello there"))

	if len(tb.responses.calls) != 1 {
		t.Fatalf("expected one completion call, got %d", len(tb.responses.calls))
	}
	req := tb.responses.calls[0]
	// Literal values on purpose: a model swap must update this test deliberately.
	if req.Model != "gpt-6-luna" {
		t.Errorf("model = %q, want gpt-6-luna", req.Model)
	}
	if req.Reasoning.Effort != "low" {
		t.Errorf("reasoning effort = %q, want low", req.Reasoning.Effort)
	}
	if req.Instructions.Value != testSysInst {
		t.Errorf("instructions = %q", req.Instructions.Value)
	}
	items := req.Input.OfInputItemList
	// Two base prompt examples plus the user's message; no thread context.
	if len(items) != 3 {
		t.Fatalf("expected 3 input items, got %d", len(items))
	}
	if got := items[2].OfMessage.Content.OfString.Value; got != " hello there" {
		t.Errorf("user prompt = %q", got)
	}
	if got := tb.discord.sentMessages(); len(got) != 1 || got[0].Channel != channelID || got[0].Content != "general kenobi" {
		t.Errorf("unexpected Discord messages %+v", got)
	}
	if got := tb.discord.typing; len(got) != 1 || got[0] != channelID {
		t.Errorf("expected a typing indicator on %s, got %v", channelID, got)
	}
	want := []storedMessage{
		{channelID, "alice (user-1) on guild-1", "User:  hello there"},
		{channelID, "Bot", "general kenobi"},
	}
	if got := tb.threads.addedMessages(); !equalStored(got, want) {
		t.Errorf("stored %+v, want %+v", got, want)
	}
}

func TestChatCompletionStorageFailureIsNotFatal(t *testing.T) {
	tb := newTestBot(t)
	tb.threads.addErr = errors.New("dynamo down")
	tb.responses.fn = func(context.Context, responses.ResponseNewParams) (*responses.Response, error) {
		return textResponse("still here"), nil
	}
	tb.bot.handleMessage(context.Background(), mention("hello"))
	if got := tb.discord.sentMessages(); len(got) != 1 || got[0].Content != "still here" {
		t.Fatalf("expected the reply despite the storage error, got %+v", got)
	}
}

func TestChatCompletionFailureNotifiesChannel(t *testing.T) {
	tb := newTestBot(t)
	tb.responses.fn = func(context.Context, responses.ResponseNewParams) (*responses.Response, error) {
		return nil, &openai.Error{StatusCode: http.StatusBadRequest}
	}
	tb.bot.handleMessage(context.Background(), mention("hello"))
	got := tb.discord.sentMessages()
	if len(got) != 1 || got[0].Content != "Whoops something went wrong processing that" {
		t.Fatalf("unexpected messages %+v", got)
	}
	if len(tb.threads.addedMessages()) != 0 {
		t.Error("nothing should be stored when the completion fails")
	}
}

func TestChatCompletionFatalErrorShutsDown(t *testing.T) {
	tb := newTestBot(t)
	tb.responses.fn = func(context.Context, responses.ResponseNewParams) (*responses.Response, error) {
		return nil, &openai.Error{StatusCode: http.StatusUnauthorized}
	}
	tb.bot.handleMessage(context.Background(), mention("hello"))
	if tb.shutdownCount() != 1 {
		t.Errorf("expected shutdown to be triggered once, got %d", tb.shutdownCount())
	}
	if n := tb.responses.callCount(); n != 1 {
		t.Errorf("a fatal error must not be retried, got %d calls", n)
	}
}

func TestReportErrorSurvivesDiscordFailure(t *testing.T) {
	tb := newTestBot(t)
	tb.discord.sendErr = errors.New("discord down")
	tb.responses.fn = func(context.Context, responses.ResponseNewParams) (*responses.Response, error) {
		return nil, &openai.Error{StatusCode: http.StatusBadRequest}
	}
	// Must not panic; the failure is only logged and recorded on the span.
	tb.bot.handleMessage(context.Background(), mention("hello"))
}

func TestThreadRequestCreatesThreadAndAnswersThere(t *testing.T) {
	tb := newTestBot(t)
	tb.responses.fn = func(context.Context, responses.ResponseNewParams) (*responses.Response, error) {
		return textResponse("in thread"), nil
	}
	tb.bot.handleMessage(context.Background(), mention("🧵 let's talk"))

	if len(tb.discord.threadsStarted) != 1 || tb.discord.threadsStarted[0].Name != "Conversation with alice" {
		t.Fatalf("expected one thread to be started, got %+v", tb.discord.threadsStarted)
	}
	if got := tb.discord.sentMessages(); len(got) != 1 || got[0].Channel != threadID {
		t.Fatalf("expected the reply in the new thread, got %+v", got)
	}
	if got := tb.threads.loaded; len(got) != 1 || got[0] != threadID {
		t.Errorf("expected the thread context to be loaded, got %v", got)
	}
}

func TestThreadCreationFailureAbortsSilently(t *testing.T) {
	tb := newTestBot(t)
	tb.discord.threadErr = errors.New("no permission")
	tb.bot.handleMessage(context.Background(), mention("🧵 let's talk"))
	if tb.responses.callCount() != 0 || len(tb.discord.sentMessages()) != 0 {
		t.Fatal("nothing should happen when the thread cannot be created")
	}
}

func TestExistingThreadContextIsSent(t *testing.T) {
	tb := newTestBot(t)
	tb.discord.channels[channelID] = &discordgo.Channel{ID: channelID, Type: discordgo.ChannelTypeGuildPublicThread}
	tb.threads.threads[channelID] = []storage.ConversationMessage{
		{Role: "user", Content: "earlier question"},
		{Role: "assistant", Content: "earlier answer"},
	}
	tb.responses.fn = func(context.Context, responses.ResponseNewParams) (*responses.Response, error) {
		return textResponse("ok"), nil
	}
	tb.bot.handleMessage(context.Background(), mention("follow up"))

	items := tb.responses.calls[0].Input.OfInputItemList
	if len(items) != 5 {
		t.Fatalf("expected base(2) + thread(2) + prompt(1) items, got %d", len(items))
	}
	if got := items[2].OfMessage.Content.OfString.Value; got != "earlier question" {
		t.Errorf("thread context item = %q", got)
	}
	if len(tb.discord.threadsStarted) != 0 {
		t.Error("no new thread should be started inside a thread")
	}
}

func TestThreadContextLoadFailureIsNotFatal(t *testing.T) {
	tb := newTestBot(t)
	tb.discord.channels[channelID] = &discordgo.Channel{ID: channelID, Type: discordgo.ChannelTypeGuildPublicThread}
	tb.threads.getErr = errors.New("dynamo down")
	tb.responses.fn = func(context.Context, responses.ResponseNewParams) (*responses.Response, error) {
		return textResponse("ok"), nil
	}
	tb.bot.handleMessage(context.Background(), mention("follow up"))
	if got := tb.discord.sentMessages(); len(got) != 1 || got[0].Content != "ok" {
		t.Fatalf("expected a reply, got %+v", got)
	}
}

func TestDrawGeneratesAndDispatchesImage(t *testing.T) {
	tb := newTestBot(t)
	b64 := base64.StdEncoding.EncodeToString(pngBytes)
	tb.images.generate = func(context.Context, openai.ImageGenerateParams) (*openai.ImagesResponse, error) {
		return imageResponse(b64), nil
	}

	m := mention("draw me a picture of a cat")
	tb.bot.handleMessage(context.Background(), m)

	if len(tb.images.generated) != 1 {
		t.Fatalf("expected one Generate call, got %d", len(tb.images.generated))
	}
	req := tb.images.generated[0]
	if req.Model != "gpt-image-2" {
		t.Errorf("model = %q, want gpt-image-2", req.Model)
	}
	if !strings.HasPrefix(req.Prompt, testDrawing+" ") || !strings.HasSuffix(req.Prompt, " a cat") {
		t.Errorf("prompt = %q", req.Prompt)
	}
	if req.SafetyIdentifier.Value != userID {
		t.Errorf("safety identifier = %q", req.SafetyIdentifier.Value)
	}

	sent := tb.discord.sentMessages()
	if len(sent) != 1 {
		t.Fatalf("expected one Discord message, got %+v", sent)
	}
	if sent[0].Content != "a picture I drawed" || sent[0].FileName != "danbot-drawing.png" || !bytes.Equal(sent[0].File, pngBytes) {
		t.Errorf("unexpected Discord message %+v", sent[0])
	}
	if sent[0].Reference == nil || sent[0].Reference.MessageID != m.ID {
		t.Errorf("the image should reply to the request, got reference %+v", sent[0].Reference)
	}

	stored := tb.imgStore.storedImages()
	if len(stored) != 1 || stored[0].Group != guildID || stored[0].Length != int64(len(pngBytes)) || !bytes.Equal(stored[0].Data, pngBytes) {
		t.Fatalf("unexpected S3 uploads %+v", stored)
	}

	want := []storedMessage{
		{channelID, "alice (user-1) on guild-1", req.Prompt[len(testDrawing)+1:]},
		{channelID, "Bot", "https://sillybullshit.click/guild-1/image-1"},
	}
	if got := tb.threads.addedMessages(); !equalStored(got, want) {
		t.Errorf("stored %+v, want %+v", got, want)
	}
}

func TestDrawFailureNotifiesChannel(t *testing.T) {
	tb := newTestBot(t)
	tb.images.generate = func(context.Context, openai.ImageGenerateParams) (*openai.ImagesResponse, error) {
		return nil, errors.New("nope")
	}
	tb.bot.handleMessage(context.Background(), mention("🎨 a cat"))
	got := tb.discord.sentMessages()
	if len(got) != 1 || !strings.HasPrefix(got[0].Content, "I fucked that up and threw it away. Sorry. (") {
		t.Fatalf("unexpected messages %+v", got)
	}
}

func TestDrawPromptStorageFailureIsFatal(t *testing.T) {
	tb := newTestBot(t)
	tb.threads.addErr = errors.New("dynamo down")
	tb.bot.handleMessage(context.Background(), mention("🎨 a cat"))
	if len(tb.images.generated) != 0 {
		t.Error("no image should be generated when the prompt cannot be recorded")
	}
}

func TestDrawFatalErrorShutsDown(t *testing.T) {
	tb := newTestBot(t)
	tb.images.generate = func(context.Context, openai.ImageGenerateParams) (*openai.ImagesResponse, error) {
		return nil, &openai.Error{StatusCode: http.StatusTooManyRequests, Code: "insufficient_quota"}
	}
	tb.bot.handleMessage(context.Background(), mention("🎨 a cat"))
	if tb.shutdownCount() != 1 {
		t.Errorf("expected shutdown, got %d", tb.shutdownCount())
	}
}

func TestDispatchRejectsInvalidBase64(t *testing.T) {
	tb := newTestBot(t)
	tb.images.generate = func(context.Context, openai.ImageGenerateParams) (*openai.ImagesResponse, error) {
		return imageResponse("abc"), nil
	}
	tb.bot.handleMessage(context.Background(), mention("🎨 a cat"))
	got := tb.discord.sentMessages()
	if len(got) != 1 || got[0].File != nil || !strings.Contains(got[0].Content, "invalid base64") {
		t.Fatalf("expected an error message and no image, got %+v", got)
	}
}

func TestEditUsesAttachment(t *testing.T) {
	tb := newTestBot(t)
	const srcURL = "https://cdn.example/source.png"
	tb.imgStore.sources[srcURL] = pngBytes
	edited := append([]byte("\x89PNG\r\n\x1a\nEDITED"), pngBytes...)
	tb.images.edit = func(_ context.Context, p openai.ImageEditParams) (*openai.ImagesResponse, error) {
		return imageResponse(base64.StdEncoding.EncodeToString(edited)), nil
	}

	m := mention("edit: add a hat")
	m.Attachments = []*discordgo.MessageAttachment{{URL: srcURL, Filename: "source.png"}}
	tb.bot.handleMessage(context.Background(), m)

	if len(tb.images.edited) != 1 {
		t.Fatalf("expected one Edit call, got %d", len(tb.images.edited))
	}
	req := tb.images.edited[0]
	if req.Model != "gpt-image-2" {
		t.Errorf("model = %q, want gpt-image-2", req.Model)
	}
	if req.Prompt != testDrawing+" add a hat" {
		t.Errorf("prompt = %q", req.Prompt)
	}
	body, err := io.ReadAll(req.Image.OfFile)
	if err != nil || !bytes.Equal(body, pngBytes) {
		t.Errorf("edit source = %v / %v", body, err)
	}

	sent := tb.discord.sentMessages()
	if len(sent) != 1 || sent[0].Content != "here ya go" || !bytes.Equal(sent[0].File, edited) {
		t.Fatalf("unexpected Discord messages %+v", sent)
	}
	added := tb.threads.addedMessages()
	if len(added) == 0 || added[0].Message != "edit: add a hat (source: "+srcURL+")" {
		t.Errorf("unexpected stored messages %+v", added)
	}
}

func TestEditRejectsUnsupportedSource(t *testing.T) {
	tb := newTestBot(t)
	const srcURL = "https://cdn.example/source.gif"
	tb.imgStore.sources[srcURL] = []byte("GIF89a.........")
	m := mention("edit: add a hat")
	m.Attachments = []*discordgo.MessageAttachment{{URL: srcURL, ContentType: "image/gif"}}
	tb.bot.handleMessage(context.Background(), m)
	if len(tb.images.edited) != 0 {
		t.Error("unsupported formats must not be sent to OpenAI")
	}
	if got := tb.discord.sentMessages(); len(got) != 1 || !strings.Contains(got[0].Content, "not a supported format") {
		t.Errorf("unexpected messages %+v", got)
	}
}

func TestEditSourceFetchFailure(t *testing.T) {
	tb := newTestBot(t)
	tb.imgStore.getErr = errors.New("404")
	m := mention("edit: add a hat")
	m.Attachments = []*discordgo.MessageAttachment{{URL: "https://cdn.example/x.png", Filename: "x.png"}}
	tb.bot.handleMessage(context.Background(), m)
	if len(tb.images.edited) != 0 {
		t.Error("no edit should be requested without a source image")
	}
	if got := tb.discord.sentMessages(); len(got) != 1 || !strings.Contains(got[0].Content, "failed to fetch source image") {
		t.Errorf("unexpected messages %+v", got)
	}
}

func TestFindEditSourceImage(t *testing.T) {
	botAuthor := &discordgo.User{ID: botID}
	img := func(url string) []*discordgo.MessageAttachment {
		return []*discordgo.MessageAttachment{{URL: url, Filename: "a.png"}}
	}
	tests := []struct {
		name  string
		setup func(*fakeDiscord, *discordgo.MessageCreate)
		want  string
	}{
		{"none", func(*fakeDiscord, *discordgo.MessageCreate) {}, ""},
		{"own attachment", func(_ *fakeDiscord, m *discordgo.MessageCreate) {
			m.Attachments = []*discordgo.MessageAttachment{{URL: "doc", Filename: "a.txt"}, img("own")[0]}
		}, "own"},
		{"inline reply to the bot", func(_ *fakeDiscord, m *discordgo.MessageCreate) {
			m.ReferencedMessage = &discordgo.Message{Author: botAuthor, Attachments: img("ref")}
		}, "ref"},
		{"reply to someone else", func(_ *fakeDiscord, m *discordgo.MessageCreate) {
			m.ReferencedMessage = &discordgo.Message{Author: &discordgo.User{ID: "other"}, Attachments: img("ref")}
		}, ""},
		{"reply without author", func(_ *fakeDiscord, m *discordgo.MessageCreate) {
			m.ReferencedMessage = &discordgo.Message{Attachments: img("ref")}
		}, ""},
		{"fetched reference", func(f *fakeDiscord, m *discordgo.MessageCreate) {
			m.MessageReference = &discordgo.MessageReference{ChannelID: channelID, MessageID: "old"}
			f.messages["old"] = &discordgo.Message{Author: botAuthor, Attachments: img("fetched")}
		}, "fetched"},
		{"fetch failure", func(_ *fakeDiscord, m *discordgo.MessageCreate) {
			m.MessageReference = &discordgo.MessageReference{ChannelID: channelID, MessageID: "missing"}
		}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tb := newTestBot(t)
			m := mention("x")
			tt.setup(tb.discord, m)
			if got := tb.bot.findEditSourceImage(m); got != tt.want {
				t.Errorf("findEditSourceImage = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHandleFatalOpenAIErrorWithoutShutdown(t *testing.T) {
	tb := newTestBot(t)
	tb.bot.shutdown = nil
	// Must not panic when no shutdown function is configured.
	tb.bot.handleFatalOpenAIError(context.Background(), &openai.Error{StatusCode: http.StatusUnauthorized})
}

func equalStored(a, b []storedMessage) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestChatCompletionRetriesEmptyResponse(t *testing.T) {
	tb := newTestBot(t)
	tb.responses.fn = func(context.Context, responses.ResponseNewParams) (*responses.Response, error) {
		return textResponse(""), nil
	}
	tb.bot.handleMessage(context.Background(), mention("hello"))
	if n := tb.responses.callCount(); n != 3 {
		t.Errorf("expected 3 attempts for an empty response, got %d", n)
	}
	if got := tb.discord.sentMessages(); len(got) != 1 || got[0].Content != "Whoops something went wrong processing that" {
		t.Errorf("unexpected messages %+v", got)
	}
}

func TestReadyAndShutdownNotifyOperator(t *testing.T) {
	tb := newTestBot(t)
	tb.bot.ReadyHandler(nil, &discordgo.Ready{})
	tb.bot.Shutdown()
	got := tb.discord.sentMessages()
	if len(got) != 2 || got[0].Content != "Gah! I'm awake! How long was I out?!" || got[1].Content != "Here I go, shutting down again!" {
		t.Fatalf("unexpected messages %+v", got)
	}
	for _, msg := range got {
		if msg.Channel != "1091532074495787049" {
			t.Errorf("operator message sent to %q", msg.Channel)
		}
	}

	// A failed send is only logged.
	tb.discord.sendErr = errors.New("discord down")
	tb.bot.ReadyHandler(nil, &discordgo.Ready{})
	tb.bot.Shutdown()
}

func TestSessionDiscordReadsState(t *testing.T) {
	state := discordgo.NewState()
	state.User = &discordgo.User{ID: botID}
	if err := state.GuildAdd(&discordgo.Guild{ID: guildID}); err != nil {
		t.Fatal(err)
	}
	thread := &discordgo.Channel{ID: threadID, GuildID: guildID, Type: discordgo.ChannelTypeGuildPublicThread}
	if err := state.ChannelAdd(thread); err != nil {
		t.Fatal(err)
	}
	s := sessionDiscord{&discordgo.Session{State: state}}
	if got := s.BotUser(); got == nil || got.ID != botID {
		t.Errorf("BotUser = %+v", got)
	}
	if ch, err := s.StateChannel(threadID); err != nil || !ch.IsThread() {
		t.Errorf("StateChannel = %+v, %v", ch, err)
	}
	if _, err := s.StateChannel("missing"); err == nil {
		t.Error("expected an error for an unknown channel")
	}
}
