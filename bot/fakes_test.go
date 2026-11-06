package bot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"openai-discord-bot/bot/storage"

	"github.com/bwmarrin/discordgo"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
)

func TestMain(m *testing.M) {
	// Keep test output readable: the bot logs liberally at every level.
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	os.Exit(m.Run())
}

const (
	botID       = "bot-1"
	userID      = "user-1"
	channelID   = "chan-1"
	guildID     = "guild-1"
	threadID    = "thread-1"
	testSysInst = "you are danbo"
	testDrawing = "in the danbo style:"
	// Operator settings deliberately differ from the production defaults so
	// the tests fail if the bot ignores its configuration.
	testOperatorChannel = "operator-chan-1"
	testImageBaseURL    = "https://images.test"
)

// pngBytes is a minimal payload that http.DetectContentType sniffs as PNG.
var pngBytes = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0xAB}, 64)...)

// callLog records every external call the bot makes and whether its context
// carried a deadline. All fakes of one testBot share one callLog.
type callLog struct {
	mu         sync.Mutex
	calls      []string
	noDeadline []string
}

func (c *callLog) record(name string, ctx context.Context) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, name)
	if ctx == nil {
		c.noDeadline = append(c.noDeadline, name)
		return
	}
	if _, ok := ctx.Deadline(); !ok {
		c.noDeadline = append(c.noDeadline, name)
	}
}

func (c *callLog) snapshot() (calls, noDeadline []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.calls...), append([]string(nil), c.noDeadline...)
}

// discordCtx returns the context that opts would attach to a Discord request,
// or nil when none of them sets one.
func discordCtx(opts []discordgo.RequestOption) context.Context {
	if len(opts) == 0 {
		return nil
	}
	cfg := &discordgo.RequestConfig{Request: httptest.NewRequest("GET", "http://discord.invalid", nil)}
	for _, opt := range opts {
		opt(cfg)
	}
	ctx := cfg.Request.Context()
	if ctx == context.Background() {
		return nil
	}
	return ctx
}

// sentMessage is one message the fake Discord accepted.
type sentMessage struct {
	Channel   string
	Content   string
	Reference *discordgo.MessageReference
	FileName  string
	File      []byte
}

// fakeDiscord is an in-memory discordAPI. Every field is guarded by mu.
type fakeDiscord struct {
	mu  sync.Mutex
	log *callLog

	self     *discordgo.User
	channels map[string]*discordgo.Channel // StateChannel results
	messages map[string]*discordgo.Message // ChannelMessage results, by message ID

	sendErr        error
	sendComplexErr error
	threadErr      error
	// onSend, when set, runs at the start of ChannelMessageSend.
	onSend func()
	// onSendComplex, when set, runs at the start of ChannelMessageSendComplex.
	onSendComplex func()

	sent           []sentMessage
	typing         []string
	threadsStarted []*discordgo.ThreadStart
	fetched        []string
}

func newFakeDiscord() *fakeDiscord {
	return &fakeDiscord{
		self:     &discordgo.User{ID: botID, Username: "danbot"},
		channels: map[string]*discordgo.Channel{},
		messages: map[string]*discordgo.Message{},
	}
}

func (f *fakeDiscord) BotUser() *discordgo.User {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.self
}

func (f *fakeDiscord) StateChannel(id string) (*discordgo.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ch, ok := f.channels[id]; ok {
		return ch, nil
	}
	return nil, discordgo.ErrStateNotFound
}

func (f *fakeDiscord) ChannelTyping(id string, opts ...discordgo.RequestOption) error {
	f.log.record("discord.ChannelTyping", discordCtx(opts))
	f.mu.Lock()
	defer f.mu.Unlock()
	f.typing = append(f.typing, id)
	return nil
}

func (f *fakeDiscord) ChannelMessage(_, messageID string, opts ...discordgo.RequestOption) (*discordgo.Message, error) {
	f.log.record("discord.ChannelMessage", discordCtx(opts))
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fetched = append(f.fetched, messageID)
	if msg, ok := f.messages[messageID]; ok {
		return msg, nil
	}
	return nil, errors.New("unknown message")
}

func (f *fakeDiscord) ChannelMessageSend(id, content string, opts ...discordgo.RequestOption) (*discordgo.Message, error) {
	f.log.record("discord.ChannelMessageSend", discordCtx(opts))
	f.mu.Lock()
	onSend := f.onSend
	f.mu.Unlock()
	if onSend != nil {
		onSend()
	}
	if ctx := discordCtx(opts); ctx != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return nil, f.sendErr
	}
	f.sent = append(f.sent, sentMessage{Channel: id, Content: content})
	return &discordgo.Message{ChannelID: id, Content: content}, nil
}

// ChannelMessageSendComplex reads the attached file to EOF, as the real
// multipart upload does. When sendComplexErr is set it fails without reading
// anything, like a request rejected before the body is sent.
func (f *fakeDiscord) ChannelMessageSendComplex(id string, data *discordgo.MessageSend, opts ...discordgo.RequestOption) (*discordgo.Message, error) {
	f.log.record("discord.ChannelMessageSendComplex", discordCtx(opts))
	f.mu.Lock()
	err := f.sendComplexErr
	onSendComplex := f.onSendComplex
	f.mu.Unlock()
	if onSendComplex != nil {
		onSendComplex()
	}
	if err != nil {
		return nil, err
	}
	if ctx := discordCtx(opts); ctx != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	msg := sentMessage{Channel: id, Content: data.Content, Reference: data.Reference}
	if data.File != nil {
		body, readErr := io.ReadAll(data.File.Reader)
		if readErr != nil {
			return nil, fmt.Errorf("reading attachment: %w", readErr)
		}
		msg.FileName = data.File.Name
		msg.File = body
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, msg)
	return &discordgo.Message{ChannelID: id, Content: data.Content}, nil
}

func (f *fakeDiscord) MessageThreadStartComplex(_, _ string, data *discordgo.ThreadStart, opts ...discordgo.RequestOption) (*discordgo.Channel, error) {
	f.log.record("discord.MessageThreadStartComplex", discordCtx(opts))
	f.mu.Lock()
	defer f.mu.Unlock()
	f.threadsStarted = append(f.threadsStarted, data)
	if f.threadErr != nil {
		return nil, f.threadErr
	}
	return &discordgo.Channel{ID: threadID, Type: discordgo.ChannelTypeGuildPublicThread}, nil
}

func (f *fakeDiscord) sentMessages() []sentMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentMessage(nil), f.sent...)
}

// storedMessage is one AddThreadMessage call.
type storedMessage struct {
	Thread, Source, Message string
}

// fakeThreadStore is an in-memory threadStore.
type fakeThreadStore struct {
	mu      sync.Mutex
	log     *callLog
	threads map[string][]storage.ConversationMessage
	getErr  error
	addErr  error
	added   []storedMessage
	loaded  []string
}

func newFakeThreadStore() *fakeThreadStore {
	return &fakeThreadStore{threads: map[string][]storage.ConversationMessage{}}
}

func (f *fakeThreadStore) GetThread(ctx context.Context, id string) ([]storage.ConversationMessage, error) {
	f.log.record("dynamo.GetThread", ctx)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loaded = append(f.loaded, id)
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.threads[id], nil
}

func (f *fakeThreadStore) AddThreadMessage(ctx context.Context, id, source, message string) error {
	f.log.record("dynamo.AddThreadMessage", ctx)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.addErr != nil {
		return f.addErr
	}
	f.added = append(f.added, storedMessage{id, source, message})
	return nil
}

func (f *fakeThreadStore) addedMessages() []storedMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]storedMessage(nil), f.added...)
}

// storedImage is one StoreImage call.
type storedImage struct {
	Group  string
	Length int64
	Data   []byte
}

// fakeImageStore is an in-memory imageStore.
type fakeImageStore struct {
	mu  sync.Mutex
	log *callLog
	// storeErr makes StoreImage fail before reading the body, like a request
	// rejected up front (bad bucket, IAM, network).
	storeErr error
	// onStore, when set, runs at the start of StoreImage (to block or panic).
	onStore func(ctx context.Context)
	stored  []storedImage
	// sources maps URL -> image bytes for GetImageFromURL.
	sources map[string][]byte
	getErr  error
	// slowBody makes GetImageFromURL return a body whose reads block until
	// the request context ends, like a stalled download.
	slowBody bool
}

// stalledBody is a response body whose Read blocks until ctx is done.
type stalledBody struct{ ctx context.Context }

func (b stalledBody) Read([]byte) (int, error) {
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}

func (stalledBody) Close() error { return nil }

func newFakeImageStore() *fakeImageStore {
	return &fakeImageStore{sources: map[string][]byte{}}
}

func (f *fakeImageStore) StoreImage(ctx context.Context, group string, r io.Reader, length int64) (string, error) {
	f.log.record("s3.StoreImage", ctx)
	f.mu.Lock()
	err, onStore := f.storeErr, f.onStore
	f.mu.Unlock()
	if onStore != nil {
		onStore(ctx)
	}
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	data, readErr := io.ReadAll(r)
	if readErr != nil {
		return "", readErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stored = append(f.stored, storedImage{group, length, data})
	return fmt.Sprintf("%s/image-%d", group, len(f.stored)), nil
}

func (f *fakeImageStore) GetImageFromURL(ctx context.Context, url string) (io.ReadCloser, int64, error) {
	f.log.record("http.GetImageFromURL", ctx)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return nil, 0, f.getErr
	}
	if f.slowBody {
		return stalledBody{ctx}, -1, nil
	}
	data, ok := f.sources[url]
	if !ok {
		return nil, 0, fmt.Errorf("no such image %q", url)
	}
	return io.NopCloser(bytes.NewReader(data)), int64(len(data)), nil
}

func (f *fakeImageStore) storedImages() []storedImage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]storedImage(nil), f.stored...)
}

// fakeImages is an in-memory imageAPI.
type fakeImages struct {
	mu        sync.Mutex
	log       *callLog
	generate  func(context.Context, openai.ImageGenerateParams) (*openai.ImagesResponse, error)
	edit      func(context.Context, openai.ImageEditParams) (*openai.ImagesResponse, error)
	generated []openai.ImageGenerateParams
	edited    []openai.ImageEditParams
}

func (f *fakeImages) Generate(ctx context.Context, p openai.ImageGenerateParams, _ ...option.RequestOption) (*openai.ImagesResponse, error) {
	f.log.record("openai.Images.Generate", ctx)
	f.mu.Lock()
	f.generated = append(f.generated, p)
	fn := f.generate
	f.mu.Unlock()
	if fn == nil {
		return nil, errors.New("unexpected Generate call")
	}
	return fn(ctx, p)
}

func (f *fakeImages) Edit(ctx context.Context, p openai.ImageEditParams, _ ...option.RequestOption) (*openai.ImagesResponse, error) {
	f.log.record("openai.Images.Edit", ctx)
	f.mu.Lock()
	f.edited = append(f.edited, p)
	fn := f.edit
	f.mu.Unlock()
	if fn == nil {
		return nil, errors.New("unexpected Edit call")
	}
	return fn(ctx, p)
}

// fakeResponses is an in-memory responsesAPI.
type fakeResponses struct {
	mu    sync.Mutex
	log   *callLog
	fn    func(context.Context, responses.ResponseNewParams) (*responses.Response, error)
	calls []responses.ResponseNewParams
}

func (f *fakeResponses) New(ctx context.Context, p responses.ResponseNewParams, _ ...option.RequestOption) (*responses.Response, error) {
	f.log.record("openai.Responses.New", ctx)
	f.mu.Lock()
	f.calls = append(f.calls, p)
	fn := f.fn
	f.mu.Unlock()
	if fn == nil {
		return nil, errors.New("unexpected Responses.New call")
	}
	return fn(ctx, p)
}

func (f *fakeResponses) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// textResponse builds a Responses API result whose OutputText is text.
func textResponse(text string) *responses.Response {
	return &responses.Response{Output: []responses.ResponseOutputItemUnion{{
		Type: "message",
		Content: []responses.ResponseOutputMessageContentUnion{
			{Type: "output_text", Text: text},
		},
	}}}
}

// imageResponse builds an image API result carrying b64 as its only image.
func imageResponse(b64 string) *openai.ImagesResponse {
	return &openai.ImagesResponse{Data: []openai.Image{{B64JSON: b64}}}
}

// testBot bundles an AIBot with the fakes behind it.
type testBot struct {
	bot       *AIBot
	discord   *fakeDiscord
	threads   *fakeThreadStore
	imgStore  *fakeImageStore
	images    *fakeImages
	responses *fakeResponses
	calls     *callLog
	shutdowns int
	mu        sync.Mutex
}

func newTestBot(t *testing.T) *testBot {
	t.Helper()
	calls := &callLog{}
	tb := &testBot{
		discord:   newFakeDiscord(),
		threads:   newFakeThreadStore(),
		imgStore:  newFakeImageStore(),
		images:    &fakeImages{log: calls},
		responses: &fakeResponses{log: calls},
		calls:     calls,
	}
	tb.discord.log = calls
	tb.threads.log = calls
	tb.imgStore.log = calls
	tb.bot = &AIBot{
		images:              tb.images,
		responses:           tb.responses,
		botCtx:              context.Background(),
		shutdown:            func() { tb.mu.Lock(); tb.shutdowns++; tb.mu.Unlock() },
		discord:             tb.discord,
		systemInstructions:  testSysInst,
		drawingInstructions: testDrawing,
		basePrompt: []storage.ConversationMessage{
			{Role: "user", Content: "example question"},
			{Role: "assistant", Content: "example answer"},
		},
		storage:           tb.threads,
		imageStorage:      tb.imgStore,
		timeouts:          defaultTimeouts(),
		operatorChannelID: testOperatorChannel,
		imageBaseURL:      testImageBaseURL,
	}
	return tb
}

func (tb *testBot) shutdownCount() int {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	return tb.shutdowns
}

// mention builds a message from userID in channelID that mentions the bot.
func mention(content string) *discordgo.MessageCreate {
	return &discordgo.MessageCreate{Message: &discordgo.Message{
		ID:        "msg-1",
		ChannelID: channelID,
		GuildID:   guildID,
		Content:   "<@" + botID + "> " + content,
		Author:    &discordgo.User{ID: userID, Username: "alice"},
		Mentions:  []*discordgo.User{{ID: botID}},
	}}
}
