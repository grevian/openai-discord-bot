package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"openai-discord-bot/bot/feedback"
	"openai-discord-bot/bot/flags"
	"openai-discord-bot/bot/storage"

	"github.com/bwmarrin/discordgo"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

// Settings are the operator-specific values the bot needs at runtime. They
// are loaded and validated by the config package.
type Settings struct {
	// OperatorChannelID is the Discord channel that receives the startup and
	// shutdown notices.
	OperatorChannelID string
	// ImageBaseURL is the public origin that archived images are served from,
	// without a trailing slash. An image key is appended to it.
	ImageBaseURL string
	// KagiAPIKey authenticates link summaries with the Kagi Extract API.
	// Empty disables them: the summarise trigger then says so.
	KagiAPIKey string
	// OwnerUserID is the Discord user allowed to change feature flags by
	// DM. Empty turns the flag command off.
	OwnerUserID string
}

type AIBot struct {
	images    imageAPI
	responses responsesAPI
	botCtx    context.Context
	// degraded latches when OpenAI reports a fatal error (exhausted quota,
	// bad key). Requests that need OpenAI then get msgDegraded without a
	// call; the latch clears on restart or by the retry_openai flag.
	degraded            atomic.Bool
	discord             discordAPI
	systemInstructions  string
	drawingInstructions string
	editInstructions    string
	basePrompt          []storage.ConversationMessage
	storage             threadStore
	imageStorage        imageStore
	timeouts            timeouts
	// imageSlots limits concurrent image generate and edit requests. Nil
	// means no limit.
	imageSlots        imageSlots
	operatorChannelID string
	imageBaseURL      string
	// gallery publishes images to the public gallery. Nil disables it.
	gallery galleryPublisher
	// links extracts page text for link summaries. Nil means summaries are
	// not configured.
	links linkExtractor
	// linkExamples are in-character reactions to example pages, sent before
	// a link summary request to show the voice wanted.
	linkExamples []linkExample
	// flags holds the feature flags. Never nil: NewAIBot falls back to
	// defaults only.
	flags flagStore
	// ownerUserID may change flags by DM. Empty disables the command.
	ownerUserID string
	// feedback stores feedback and ideas. Nil means it is not configured.
	feedback        feedbackStore
	feedbackLimiter *feedback.Limiter
	// galleryWG tracks in-flight gallery publishes.
	galleryWG sync.WaitGroup

	// readyOnce makes the startup notice go out once per process: discordgo
	// fires Ready again after every reconnect.
	readyOnce sync.Once

	// lifeMu guards draining; inflight counts handlers admitted by
	// beginRequest and not yet finished.
	lifeMu   sync.Mutex
	draining bool
	inflight sync.WaitGroup
	// handlerCtx is the base of every message handler's context; Drain
	// cancels it when the wait times out. Created lazily by beginRequest.
	handlerCtx    context.Context
	handlerCancel context.CancelFunc
}

// promptFile is the on-disk format of prompts/danbo.json.
type promptFile struct {
	Instructions        string                        `json:"instructions"`
	DrawingInstructions string                        `json:"drawing_instructions"`
	EditInstructions    string                        `json:"edit_instructions"`
	Examples            []storage.ConversationMessage `json:"examples"`
	LinkExamples        []linkExample                 `json:"link_examples"`
}

// loadPromptFile reads and parses the bot's prompt file.
func loadPromptFile(path string) (promptFile, error) {
	var pf promptFile
	promptBytes, err := os.ReadFile(path)
	if err != nil {
		return pf, fmt.Errorf("failed to read initial prompt: %w", err)
	}
	if err := json.Unmarshal(promptBytes, &pf); err != nil {
		return pf, fmt.Errorf("failed to parse initial prompt: %w", err)
	}
	return pf, nil
}

// Options is everything NewAIBot needs. Flags may be nil (defaults only).
type Options struct {
	OpenAI       openai.Client
	Discord      *discordgo.Session
	Storage      *storage.Storage
	ImageStorage *storage.ImageStorage
	Settings     Settings
	Flags        *flags.Flags
	// Feedback may be nil (feedback is then not switched on).
	Feedback *feedback.Store
	// PromptPath is the persona file (prompts/danbo.json).
	PromptPath string
}

func NewAIBot(botCtx context.Context, opts Options) (*AIBot, error) {
	aiClient, discordSession, threadStorage, imageStorage := opts.OpenAI, opts.Discord, opts.Storage, opts.ImageStorage
	settings, flagCache := opts.Settings, opts.Flags
	prompt, err := loadPromptFile(opts.PromptPath)
	if err != nil {
		return nil, err
	}

	bot := &AIBot{
		discord:             sessionDiscord{discordSession},
		images:              leanImages{&aiClient.Images},
		responses:           &aiClient.Responses,
		botCtx:              botCtx,
		systemInstructions:  prompt.Instructions,
		drawingInstructions: prompt.DrawingInstructions,
		editInstructions:    prompt.EditInstructions,
		basePrompt:          prompt.Examples,
		linkExamples:        prompt.LinkExamples,
		storage:             threadStorage,
		imageStorage:        imageStorage,
		timeouts:            defaultTimeouts(),
		imageSlots:          newImageSlots(defaultMaxConcurrentImages),
		operatorChannelID:   settings.OperatorChannelID,
		imageBaseURL:        settings.ImageBaseURL,
		flags:               flagCache,
		ownerUserID:         settings.OwnerUserID,
	}
	bot.feedbackLimiter = feedback.NewLimiter(feedbackLimitCount, feedbackLimitWindow)
	// Only a non-nil store: a nil *Store in the interface would not be nil.
	if opts.Feedback != nil {
		bot.feedback = opts.Feedback
	}
	if flagCache == nil {
		bot.flags = NewFlags(nil)
	}
	if settings.OwnerUserID == "" {
		slog.Warn("BOT_OWNER_USER_ID is not set, the flag command is disabled")
	}
	if settings.KagiAPIKey != "" {
		bot.links = newKagiClient(settings.KagiAPIKey)
	} else {
		slog.Warn("BOT_KAGI_API_KEY is not set, link summaries are disabled")
	}

	// TODO Wire up more handlers
	discordSession.AddHandler(bot.ReadyHandler)
	discordSession.AddHandler(bot.messageCreate)

	return bot, nil
}

// SetGallery enables publishing generated images to the public gallery.
func (b *AIBot) SetGallery(g galleryPublisher) {
	b.gallery = g
}

// messageCreate is the Discord MessageCreate handler. Each message gets its
// own deadline so a hung upstream cannot pin the handler goroutine forever.
func (b *AIBot) messageCreate(_ *discordgo.Session, m *discordgo.MessageCreate) {
	defer recoverHandler("messageCreate")
	base, ok := b.beginRequest()
	if !ok {
		slog.Default().WithGroup("messageCreate").Info("Shutting down; ignoring new message")
		return
	}
	defer b.inflight.Done()
	ctx, cancel := withTimeout(base, b.timeouts.Request)
	defer cancel()
	b.handleMessage(ctx, m)
}

// recoverHandler stops a panic in a Discord event handler from crashing the
// process: discordgo runs handlers in plain goroutines without recover().
// Call it deferred, directly, at the top of each handler.
func recoverHandler(handler string) {
	if r := recover(); r != nil {
		slog.Default().Error("recovered from panic in Discord handler",
			slog.String("handler", handler),
			slog.Any("panic", r),
			slog.String("stack", string(debug.Stack())))
	}
}

func (b *AIBot) handleMessage(ctx context.Context, m *discordgo.MessageCreate) {
	logger := slog.Default().WithGroup("messageCreate")
	self := b.discord.BotUser()
	if m.Author.ID == self.ID {
		return
	}

	// Owner flag commands come by DM, with or without a mention, so they
	// are routed before the mention check.
	if m.GuildID == "" && b.handleFlagDM(ctx, m, self.ID) {
		return
	}

	if !userWasMentioned(self, m.Mentions) {
		return
	}

	ctx, span := otel.GetTracerProvider().Tracer("AIBot").Start(ctx, "messageCreate")
	span.SetAttributes(
		attribute.String("user", m.Author.ID),
		attribute.String("guild", m.GuildID),
		attribute.String("channel", m.ChannelID),
	)
	defer span.End()

	logger.InfoContext(ctx, "Processing Message", slog.String("message", m.Content))

	// Figure out if we should be acting in a thread
	responseChannel, threadPromptContext, err := b.handleThreading(ctx, m)
	if err != nil {
		logger.ErrorContext(ctx, "failed to load or create thread context", slog.Any("error", err))
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
		return
	}
	logger.DebugContext(ctx, "loaded thread context", slog.Int("thread_length", len(threadPromptContext)))
	threadChars := 0
	for _, tm := range threadPromptContext {
		threadChars += utf8.RuneCountInString(tm.Content)
	}
	span.SetAttributes(
		attribute.Int("thread.messages", len(threadPromptContext)),
		attribute.Int("thread.chars", threadChars),
	)

	// Let users know we're "typing", the call to OpenAI can take a few seconds
	// A single ChannelTyping call lasts about 10 seconds, so keep refreshing it
	// until the reply is sent. The deferred stop covers every return path,
	// including panics; the success paths stop it earlier, right after the
	// reply goes out.
	ctx, stopTyping := b.keepTyping(ctx, responseChannel)
	defer stopTyping()

	source := b.findEditSource(ctx, m)
	intent, prompt := classifyMessage(m.Content, self.ID, source.url != "")
	// Clean is asked for with 🫧 in the message text (not as a reaction), and
	// is sticky: an edit of a clean image stays clean.
	clean := hasCleanEmoji(m.Content) || (intent == intentEdit && source.clean)
	span.SetAttributes(attribute.String("intent", intent.String()), attribute.Bool("clean", clean))

	if b.isDegraded(ctx) && intent != intentHelp && intent != intentEditNoImage &&
		intent != intentFeedback && intent != intentIdea {
		span.SetAttributes(attribute.Bool("degraded", true))
		stopTyping()
		err = b.sendMessage(ctx, responseChannel, msgDegraded)
		if err != nil {
			b.reportError(ctx, span, responseChannel, msgGeneric, err)
		}
		return
	}

	switch intent {
	case intentHelp:
		err = b.sendMessage(ctx, responseChannel, helpText(b.imageBaseURL))
	case intentEditNoImage:
		err = b.sendMessage(ctx, responseChannel, msgEditNeedsImage)
	case intentFeedback, intentIdea:
		err = b.handleFeedback(ctx, responseChannel, intent, prompt, m)
	case intentEdit:
		err = b.handleImageEditMessage(ctx, responseChannel, prompt, source, clean, threadPromptContext, m)
	case intentDraw:
		err = b.handleImageMessage(ctx, responseChannel, prompt, clean, m)
	case intentSummarize:
		err = b.handleSummarizeMessage(ctx, responseChannel, prompt, m)
	default:
		err = b.handleCompletionPrompt(ctx, responseChannel, prompt, threadPromptContext, m)
	}
	// Stop before posting anything so the indicator cannot reappear after
	// the final message.
	stopTyping()
	if err != nil {
		b.reportError(ctx, span, responseChannel, failureMessage(err), err)
		return
	}
	span.SetStatus(codes.Ok, "Success")
}

// Handle a text completion prompt, including applying existing thread context and updating the stored state of that context
func (b *AIBot) handleCompletionPrompt(ctx context.Context, responseChannel string, sanitizedUserPrompt string, threadPromptContext []storage.ConversationMessage, m *discordgo.MessageCreate) error {
	var err error
	logger := slog.Default().WithGroup("handleCompletionPrompt")
	ctx, span := otel.GetTracerProvider().Tracer("AIBot").Start(ctx, "handleCompletionPrompt")
	defer span.End()

	inputItems := make([]responses.ResponseInputItemUnionParam, 0, len(b.basePrompt)+len(threadPromptContext)+1)
	addMsg := func(role, content string) {
		inputItems = append(inputItems, responses.ResponseInputItemParamOfMessage(
			content,
			responses.EasyInputMessageRole(role),
		))
	}
	for _, msg := range b.basePrompt {
		addMsg(msg.Role, msg.Content)
	}
	for _, msg := range capChatContext(threadPromptContext, maxChatContextChars) {
		addMsg(msg.Role, msg.Content)
	}
	addMsg("user", sanitizedUserPrompt)

	request := responses.ResponseNewParams{
		Model:        chatModel,
		Instructions: openai.String(b.systemInstructions + "\n\n" + lengthHint),
		Reasoning:    shared.ReasoningParam{Effort: chatReasoningEffort},
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: inputItems,
		},
	}

	// Transient failures (429, 5xx, connection errors) are retried by the
	// OpenAI SDK itself (2 retries by default). Don't add a second retry layer
	// on top: that multiplies the calls, and it retried non-retryable 4xx too.
	openaiCtx, cancel := withTimeout(ctx, b.timeouts.OpenAIText)
	response, err := b.responsesNew(openaiCtx, opChat, request)
	cancel()
	if err != nil {
		if isFatalOpenAIError(err) {
			b.handleFatalOpenAIError(ctx, err)
		}
		logger.ErrorContext(ctx, "Failed to retrieve completion from OpenAI", slog.Any("error", err))
		return fmt.Errorf("failed to get response from openai: %w", err)
	}
	responseText := response.OutputText()
	if responseText == "" {
		logger.WarnContext(ctx, "Empty response text from OpenAI", slog.Any("response", response))
		return errors.New("failed to get response from openai: received an empty response")
	}

	// Discord rejects content over 2000 characters: split (and cap) the reply
	// first so the stored thread matches what is actually posted.
	chunks := splitReply(responseText, discordMessageLimit, maxReplyMessages)
	responseText = strings.Join(chunks, "\n\n")

	// TODO It's weird that we're modifying the stored thread state here, but loaded it elsewhere
	err = b.recordUser(ctx, responseChannel, m, sanitizedUserPrompt)
	if err != nil {
		warnErr := fmt.Errorf("failed to record conversation message: %w", err)
		span.RecordError(warnErr)
		logger.WarnContext(ctx, "non-fatal error updating thread context", slog.Any("error", warnErr))
	}

	err = b.recordBot(ctx, responseChannel, responseText)
	if err != nil {
		warnErr := fmt.Errorf("failed to record conversation message: %w", err)
		span.RecordError(warnErr)
		logger.WarnContext(ctx, "non-fatal error updating thread context", slog.Any("error", warnErr))
	}

	// The reply is the last step: stop the typing indicator first so it
	// cannot be refreshed while the reply is being posted.
	stopTypingFromContext(ctx)
	for _, chunk := range chunks {
		if err = b.sendMessage(ctx, responseChannel, chunk); err != nil {
			return fmt.Errorf("failed to respond to discord channel: %w", err)
		}
	}

	span.SetStatus(codes.Ok, "Success")
	return nil
}

// Create a new thread if requested, or load the context of a thread if already in one
func (b *AIBot) handleThreading(ctx context.Context, m *discordgo.MessageCreate) (responseChannel string, threadContext []storage.ConversationMessage, errResponse error) {
	logger := slog.Default().WithGroup("handleThreading")
	// Default to responding to the channel the message came from
	responseChannel = m.ChannelID
	isThreaded := false
	wantThreaded := strings.Contains(m.Content, "🧵")

	// The current "channel" may already be a thread
	if ch, err := b.discord.StateChannel(m.ChannelID); err == nil && ch.IsThread() {
		isThreaded = true
		responseChannel = ch.ID
	}

	// But if the user requested a thread, but we're not in one yet, create it
	if wantThreaded && !isThreaded {
		opt, cancel := b.discordCtx(ctx)
		ch, err := b.discord.MessageThreadStartComplex(m.ChannelID, m.ID, &discordgo.ThreadStart{
			Name:                fmt.Sprintf("Conversation with %s", m.Author.Username),
			AutoArchiveDuration: 60,
		}, opt)
		cancel()
		if err != nil {
			errResponse = fmt.Errorf("failed to create discord conversation thread: %w", err)
			return
		}
		responseChannel = ch.ID
		isThreaded = true
	}

	// If we are in a thread, we should load the thread's conversation context
	if isThreaded {
		var err error
		threadContext, err = b.getThread(ctx, responseChannel)
		if err != nil {
			// This doesn't have to be fatal, though it may be confusing
			warnErr := fmt.Errorf("failed to load thread conversation context: %w", err)
			logger.WarnContext(ctx, "Failed to load thread conversation context", slog.Any("error", warnErr), slog.String("thread_id", responseChannel))
		}
	}
	return
}
