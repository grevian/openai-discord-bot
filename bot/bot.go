package bot

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"runtime/debug"
	"strings"
	"sync"

	"openai-discord-bot/bot/storage"

	"github.com/bwmarrin/discordgo"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type AIBot struct {
	images              imageAPI
	responses           responsesAPI
	botCtx              context.Context
	shutdown            context.CancelFunc
	discord             discordAPI
	systemInstructions  string
	drawingInstructions string
	basePrompt          []storage.ConversationMessage
	storage             threadStore
	imageStorage        imageStore
	timeouts            timeouts
}

// promptFile is the on-disk format of prompts/danbo.json.
type promptFile struct {
	Instructions        string                        `json:"instructions"`
	DrawingInstructions string                        `json:"drawing_instructions"`
	Examples            []storage.ConversationMessage `json:"examples"`
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

// isFatalOpenAIError reports whether err indicates a condition that won't
// recover on retry: an exhausted quota or a revoked / invalid API key. When
// true the caller should stop calling OpenAI and trigger process shutdown so
// that ECS does not pile up a crash-loop of half-open Discord sessions.
func isFatalOpenAIError(err error) bool {
	var apiErr *openai.Error
	if !errors.As(err, &apiErr) {
		return false
	}
	if apiErr.StatusCode == http.StatusUnauthorized {
		return true
	}
	return apiErr.Code == "insufficient_quota" || apiErr.Code == "invalid_api_key"
}

func NewAIBot(botCtx context.Context, shutdown context.CancelFunc, aiClient openai.Client, discordSession *discordgo.Session, threadStorage *storage.Storage, imageStorage *storage.ImageStorage) *AIBot {
	prompt, err := loadPromptFile("prompts/danbo.json")
	if err != nil {
		log.Panic(err)
	}

	bot := &AIBot{
		discord:             sessionDiscord{discordSession},
		images:              &aiClient.Images,
		responses:           &aiClient.Responses,
		botCtx:              botCtx,
		shutdown:            shutdown,
		systemInstructions:  prompt.Instructions,
		drawingInstructions: prompt.DrawingInstructions,
		basePrompt:          prompt.Examples,
		storage:             threadStorage,
		imageStorage:        imageStorage,
		timeouts:            defaultTimeouts(),
	}

	// TODO Wire up more handlers
	discordSession.AddHandler(bot.ReadyHandler)
	discordSession.AddHandler(bot.messageCreate)

	return bot
}

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
)

// maxEditSourceBytes is the size at which a source image is refused for an
// edit. openai-go documents the GPT image models (gpt-image-1, used for edits
// here) as taking a png, webp or jpg "less than 50MB" per image, so a source of
// this many bytes or more is too large. Sized images are checked against it,
// and bodies are read through a limit of the same size.
const maxEditSourceBytes = 50_000_000

// errSourceImageTooLarge is returned when the source image to edit is at or
// over maxEditSourceBytes. The message is shown to the user as is.
var errSourceImageTooLarge = newUserError("That image is too large for me to edit (the limit is 50MB). Big files scare me too. Please send a smaller one.",
	errors.New("source image too large"))

// classifyMessage decides what a message is asking for and returns the prompt
// to send for it. botID is stripped from the prompt; hasSourceImage reports
// whether an image to edit is available.
func classifyMessage(content, botID string, hasSourceImage bool) (messageIntent, string) {
	// Strip our UserId out of messages to keep the record from being too confusing,
	sanitizedUserPrompt := strings.ReplaceAll(content, fmt.Sprintf("<@%s>", botID), "")
	lowerPrompt := strings.ToLower(sanitizedUserPrompt)

	wantsEdit := hasSourceImage && (strings.Contains(sanitizedUserPrompt, "✏️") ||
		strings.Contains(lowerPrompt, "edit:") ||
		strings.Contains(lowerPrompt, "redraw"))
	wantsDraw := strings.Contains(lowerPrompt, "🎨") || strings.Contains(lowerPrompt, "draw me a picture of")

	switch {
	case wantsEdit:
		return intentEdit, stripEditTriggers(sanitizedUserPrompt)
	case wantsDraw:
		// Strip the prompt prefix out of the message
		return intentDraw, stripDrawTriggers(sanitizedUserPrompt)
	default:
		return intentChat, sanitizedUserPrompt
	}
}

// messageCreate is the Discord MessageCreate handler. Each message gets its
// own deadline so a hung upstream cannot pin the handler goroutine forever.
func (b *AIBot) messageCreate(_ *discordgo.Session, m *discordgo.MessageCreate) {
	defer recoverHandler("messageCreate")
	ctx, cancel := withTimeout(context.Background(), b.timeouts.Request)
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

	// Let users know we're "typing", the call to OpenAI can take a few seconds
	typingOpt, cancelTyping := b.discordCtx(ctx)
	_ = b.discord.ChannelTyping(responseChannel, typingOpt)
	cancelTyping()

	sourceImageURL := b.findEditSourceImage(ctx, m)
	intent, prompt := classifyMessage(m.Content, self.ID, sourceImageURL != "")

	switch intent {
	case intentEdit:
		err = b.handleImageEditMessage(ctx, responseChannel, prompt, sourceImageURL, m)
	case intentDraw:
		err = b.handleImageMessage(ctx, responseChannel, prompt, m)
	default:
		err = b.handleCompletionPrompt(ctx, responseChannel, prompt, threadPromptContext, m)
	}
	if err != nil {
		b.reportError(ctx, span, responseChannel, failureMessage(err), err)
		return
	}
	span.SetStatus(codes.Ok, "Success")
}

// User-facing failure messages. The full error goes to the logs and spans only.
const (
	msgInvalidRequest = "OpenAI's content policy blocked that one. I've seen worse in Thunder Bay, but I can't make it. Try rewording it, maybe something less grim."
	msgBusy           = "Ugh, I'm swamped right now, and I have a feeling it'll stay that way. Please try again in a minute."
	msgGeneric        = "Gah! Something broke while I was working on that, and it's probably my fault. Please try again."
)

// userError is an error whose message is safe and useful to show to the user.
// Wrap with newUserError; detect with errors.As.
type userError struct {
	msg string
	err error
}

func (e *userError) Error() string {
	if e.err == nil {
		return e.msg
	}
	return e.err.Error()
}
func (e *userError) Unwrap() error       { return e.err }
func (e *userError) UserMessage() string { return e.msg }

// newUserError returns err annotated with a message to show the user.
func newUserError(msg string, err error) error { return &userError{msg: msg, err: err} }

// failureMessage is the text posted to the channel when handling a message
// failed with err. It never includes the error's own text.
func failureMessage(err error) string {
	var ue *userError
	if errors.As(err, &ue) {
		return ue.UserMessage()
	}
	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.StatusCode == http.StatusTooManyRequests,
			apiErr.StatusCode == http.StatusServiceUnavailable,
			apiErr.StatusCode == 529,
			apiErr.Code == "rate_limit_exceeded", apiErr.Code == "server_is_overloaded":
			if apiErr.Code != "insufficient_quota" {
				return msgBusy
			}
		case apiErr.Code == "content_policy_violation", apiErr.Code == "moderation_blocked":
			return msgInvalidRequest
		}
	}
	return msgGeneric
}

// reportError marks the span as failed and tells the channel that the request
// failed. A failure to send that notice is recorded and logged as well. The
// notice gets its own deadline, so it is still sent when the failure was the
// request running out of time.
func (b *AIBot) reportError(ctx context.Context, span trace.Span, channel, userMessage string, err error) {
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
	discordErr := b.sendMessage(context.WithoutCancel(ctx), channel, userMessage)
	if discordErr != nil {
		span.RecordError(discordErr)
		slog.Default().WithGroup("messageCreate").ErrorContext(ctx, "Failed to notify discord channel of the error", slog.Any("error", discordErr))
	}
}

func (b *AIBot) ReadyHandler(_ *discordgo.Session, _ *discordgo.Ready) {
	defer recoverHandler("ReadyHandler")
	slog.Default().WithGroup("ReadyHandler").Info("Connection state ready, Registering intents")
	err := b.sendMessage(context.Background(), "1091532074495787049", "Gah! I'm awake! How long was I out?!")
	if err != nil {
		slog.Default().Error("Failed to notify operator of the ready event", slog.Any("error", err))
	}
}

func (b *AIBot) handleImageMessage(ctx context.Context, responseChannel string, prompt string, m *discordgo.MessageCreate) error {
	var err error

	ctx, span := otel.GetTracerProvider().Tracer("AIBot").Start(ctx, "handleImageMessage")
	defer span.End()

	// Record the prompt to our thread context
	err = b.addThreadMessage(ctx, responseChannel, fmt.Sprintf("%s (%s) on %s", m.Author.Username, m.Author.ID, m.GuildID), prompt)
	if err != nil {
		return fmt.Errorf("failed to record drawing prompt: %w", err)
	}

	// Prepend the drawing system instructions so every generated image carries
	// the danbot art style, even though the image API has no system-prompt slot.
	fullPrompt := prompt
	if b.drawingInstructions != "" {
		fullPrompt = b.drawingInstructions + " " + prompt
	}

	// Request the image(s) from openAI
	imageRequest := openai.ImageGenerateParams{
		Prompt:           fullPrompt,
		N:                openai.Int(1),
		SafetyIdentifier: openai.String(m.Author.ID),
		Size:             openai.ImageGenerateParamsSize1024x1024,
		Model:            imageGenerateModel,
	}
	span.SetAttributes(
		attribute.String("model", string(imageRequest.Model)),
		attribute.String("size", string(imageRequest.Size)),
	)
	openaiCtx, cancel := withTimeout(ctx, b.timeouts.OpenAIImage)
	responseImage, err := b.images.Generate(openaiCtx, imageRequest)
	cancel()
	if err != nil {
		if isFatalOpenAIError(err) {
			b.handleFatalOpenAIError(ctx, err)
		}
		return fmt.Errorf("failed to get image from openai: %w", err)
	}
	b64, err := firstImageB64(responseImage)
	if err != nil {
		return err
	}

	if err := b.dispatchGeneratedImage(ctx, span, responseChannel, b64, "a picture I drawed", m); err != nil {
		return err
	}

	span.SetStatus(codes.Ok, "Success")
	return nil
}

// firstImageB64 returns the first image in an image API response, or an error
// when the response carries none.
func firstImageB64(resp *openai.ImagesResponse) (string, error) {
	if resp == nil || len(resp.Data) == 0 || resp.Data[0].B64JSON == "" {
		return "", errors.New("openai returned no image")
	}
	return resp.Data[0].B64JSON, nil
}

// dispatchGeneratedImage takes a base64 PNG from the OpenAI image API, sends
// it to Discord as a reply attachment, and concurrently uploads a copy to S3 +
// records the resulting URL in thread context. Shared between the generation
// and edit paths.
//
// The image is decoded once into memory (about 1-2 MB) and both uploads read
// their own reader over those bytes, so an S3 failure cannot truncate what
// Discord receives, and a Discord failure cannot block the S3 upload. Both
// finish before this returns.
func (b *AIBot) dispatchGeneratedImage(ctx context.Context, span trace.Span, responseChannel, b64, content string, m *discordgo.MessageCreate) error {
	logger := slog.Default().WithGroup("dispatchGeneratedImage")

	image, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return fmt.Errorf("invalid base64 image from openai: %w", err)
	}
	logger.DebugContext(ctx, "image retrieval", slog.Int("image_length", len(image)))

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		// This goroutine is outside the handler's recover, so it needs its own.
		defer recoverHandler("archiveImage")
		b.archiveImage(ctx, span, responseChannel, m.GuildID, image)
	}()

	opt, cancel := b.discordCtx(ctx)
	_, err = b.discord.ChannelMessageSendComplex(responseChannel, &discordgo.MessageSend{
		Content:   content,
		Reference: m.Reference(),
		File: &discordgo.File{
			Name:        "danbot-drawing.png",
			ContentType: "image/png",
			Reader:      bytes.NewReader(image),
		},
	}, opt)
	cancel()
	wg.Wait()
	if err != nil {
		return fmt.Errorf("failed to send embedded image to discord: %w", err)
	}
	return nil
}

// archiveImage stores a copy of a generated image in S3 and records its public
// URL in the thread context. Failures are recorded and logged, not returned:
// the user already has the image in Discord.
func (b *AIBot) archiveImage(ctx context.Context, span trace.Span, responseChannel, guildID string, image []byte) {
	logger := slog.Default().WithGroup("dispatchGeneratedImage")

	storeCtx, cancel := withTimeout(ctx, b.timeouts.Storage)
	imageKey, err := b.imageStorage.StoreImage(storeCtx, guildID, bytes.NewReader(image), int64(len(image)))
	cancel()
	if err != nil {
		span.RecordError(err)
		logger.ErrorContext(ctx, "failed to store a copy of the image in S3", slog.Any("error", err))
		return
	}

	imageUrl := fmt.Sprintf("https://sillybullshit.click/%s", imageKey)

	if err := b.addThreadMessage(ctx, responseChannel, "Bot", imageUrl); err != nil {
		span.RecordError(err)
		logger.ErrorContext(ctx, "failed to record image URL in thread context", slog.Any("error", err))
	}
}

// handleImageEditMessage takes a prompt and a URL to an existing image and asks
// OpenAI's image edit endpoint to produce a modified version, then ships it the
// same way handleImageMessage does for fresh generations.
func (b *AIBot) handleImageEditMessage(ctx context.Context, responseChannel, prompt, sourceURL string, m *discordgo.MessageCreate) error {
	logger := slog.Default().WithGroup("handleImageEditMessage")

	ctx, span := otel.GetTracerProvider().Tracer("AIBot").Start(ctx, "handleImageEditMessage")
	defer span.End()
	span.SetAttributes(attribute.String("source_url", sourceURL))

	if err := b.addThreadMessage(ctx, responseChannel,
		fmt.Sprintf("%s (%s) on %s", m.Author.Username, m.Author.ID, m.GuildID),
		fmt.Sprintf("edit: %s (source: %s)", prompt, sourceURL)); err != nil {
		return fmt.Errorf("failed to record edit prompt: %w", err)
	}

	srcBytes, err := b.fetchSourceImage(ctx, sourceURL)
	if err != nil {
		return err
	}
	srcContentType, srcFilename := detectImageType(srcBytes)
	logger.DebugContext(ctx, "source image fetched",
		slog.Int("bytes", len(srcBytes)),
		slog.String("content_type", srcContentType))
	if srcContentType == "" {
		return newUserError("That image isn't a supported format. Please use png, jpeg or webp. Nothing else ever works out for me anyway.", fmt.Errorf("source image is not a supported format (png/jpeg/webp)"))
	}

	fullPrompt := prompt
	if b.drawingInstructions != "" {
		fullPrompt = b.drawingInstructions + " " + prompt
	}

	// The OpenAI multipart encoder needs a recognizable filename + content type;
	// without it the form part is sent as application/octet-stream and the Edit
	// endpoint rejects it. openai.File wires both onto the io.Reader.
	editRequest := openai.ImageEditParams{
		Image:            openai.ImageEditParamsImageUnion{OfFile: openai.File(bytes.NewReader(srcBytes), srcFilename, srcContentType)},
		Prompt:           fullPrompt,
		N:                openai.Int(1),
		SafetyIdentifier: openai.String(m.Author.ID),
		Size:             openai.ImageEditParamsSize1024x1024,
		Model:            imageEditModel,
	}
	span.SetAttributes(
		attribute.String("model", string(editRequest.Model)),
		attribute.String("size", string(editRequest.Size)),
	)

	openaiCtx, cancel := withTimeout(ctx, b.timeouts.OpenAIImage)
	responseImage, err := b.images.Edit(openaiCtx, editRequest)
	cancel()
	if err != nil {
		if isFatalOpenAIError(err) {
			b.handleFatalOpenAIError(ctx, err)
		}
		return fmt.Errorf("failed to edit image via openai: %w", err)
	}
	b64, err := firstImageB64(responseImage)
	if err != nil {
		return err
	}

	if err := b.dispatchGeneratedImage(ctx, span, responseChannel, b64, "here ya go", m); err != nil {
		return err
	}

	span.SetStatus(codes.Ok, "Success")
	return nil
}

// fetchSourceImage downloads the image to edit. The deadline covers both the
// request and reading the body. A source at or over maxEditSourceBytes is
// refused: first by its reported length when there is one, then by reading at
// most maxEditSourceBytes, so a body with no length cannot grow without bound.
func (b *AIBot) fetchSourceImage(ctx context.Context, sourceURL string) ([]byte, error) {
	fetchCtx, cancel := withTimeout(ctx, b.timeouts.Storage)
	defer cancel()

	src, length, err := b.imageStorage.GetImageFromURL(fetchCtx, sourceURL)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch source image: %w", err)
	}
	defer func() { _ = src.Close() }()

	// length is -1 when the response had no Content-Length.
	if length >= maxEditSourceBytes {
		return nil, errSourceImageTooLarge
	}

	// The Edit endpoint sends the image as multipart form data, which means the
	// SDK reads the whole body up front — buffer it here so we can pass an
	// io.Reader that won't get drained partway through.
	srcBytes, err := io.ReadAll(io.LimitReader(src, maxEditSourceBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to read source image: %w", err)
	}
	// Reading stopped at the limit, so a full buffer means the body was at
	// least that big.
	if len(srcBytes) >= maxEditSourceBytes {
		return nil, errSourceImageTooLarge
	}
	return srcBytes, nil
}

// findEditSourceImage returns a URL to an image to be used as the source for an
// edit, preferring an attachment on the triggering message, then falling back
// to an attachment on the message it's replying to (if that message came from
// us). Returns "" when no candidate exists.
func (b *AIBot) findEditSourceImage(ctx context.Context, m *discordgo.MessageCreate) string {
	for _, att := range m.Attachments {
		if isImageAttachment(att) {
			return att.URL
		}
	}

	ref := m.ReferencedMessage
	if ref == nil && m.MessageReference != nil && m.MessageReference.MessageID != "" {
		// The gateway doesn't always inline ReferencedMessage — older messages
		// or cache misses fall back to an explicit fetch.
		opt, cancel := b.discordCtx(ctx)
		fetched, err := b.discord.ChannelMessage(m.MessageReference.ChannelID, m.MessageReference.MessageID, opt)
		cancel()
		if err == nil {
			ref = fetched
		}
	}
	if ref == nil || ref.Author == nil || ref.Author.ID != b.discord.BotUser().ID {
		return ""
	}
	for _, att := range ref.Attachments {
		if isImageAttachment(att) {
			return att.URL
		}
	}
	return ""
}

// detectImageType sniffs the leading bytes of an image and returns the MIME
// type + a matching filename for the formats OpenAI's image edit endpoint
// accepts (png/jpeg/webp). Returns "", "" for anything else.
func detectImageType(b []byte) (contentType, filename string) {
	sniffed := http.DetectContentType(b)
	switch sniffed {
	case "image/png":
		return sniffed, "source.png"
	case "image/jpeg":
		return sniffed, "source.jpg"
	case "image/webp":
		return sniffed, "source.webp"
	}
	return "", ""
}

func isImageAttachment(att *discordgo.MessageAttachment) bool {
	if att == nil {
		return false
	}
	if strings.HasPrefix(att.ContentType, "image/") {
		return true
	}
	lower := strings.ToLower(att.Filename)
	return strings.HasSuffix(lower, ".png") ||
		strings.HasSuffix(lower, ".jpg") ||
		strings.HasSuffix(lower, ".jpeg") ||
		strings.HasSuffix(lower, ".webp")
}

// stripDrawTriggers removes the draw triggers from a prompt, keeping the
// user's original case.
func stripDrawTriggers(prompt string) string {
	out := strings.ReplaceAll(prompt, "🎨", "")
	out = drawPhrase.ReplaceAllString(out, "")
	return strings.TrimSpace(out)
}

var drawPhrase = regexp.MustCompile(`(?i)draw me a picture of`)

func stripEditTriggers(prompt string) string {
	out := strings.ReplaceAll(prompt, "✏️", "")
	out = strings.ReplaceAll(out, "edit:", "")
	out = strings.ReplaceAll(out, "Edit:", "")
	out = strings.ReplaceAll(out, "EDIT:", "")
	out = strings.ReplaceAll(out, "redraw", "")
	out = strings.ReplaceAll(out, "Redraw", "")
	return strings.TrimSpace(out)
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
	for _, msg := range threadPromptContext {
		addMsg(msg.Role, msg.Content)
	}
	addMsg("user", sanitizedUserPrompt)

	request := responses.ResponseNewParams{
		Model:        chatModel,
		Instructions: openai.String(b.systemInstructions),
		Reasoning:    shared.ReasoningParam{Effort: chatReasoningEffort},
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: inputItems,
		},
	}

	// Transient failures (429, 5xx, connection errors) are retried by the
	// OpenAI SDK itself (2 retries by default). Don't add a second retry layer
	// on top: that multiplies the calls, and it retried non-retryable 4xx too.
	openaiCtx, cancel := withTimeout(ctx, b.timeouts.OpenAIText)
	response, err := b.responses.New(openaiCtx, request)
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

	// TODO It's weird that we're modifying the stored thread state here, but loaded it elsewhere
	err = b.addThreadMessage(ctx, responseChannel, fmt.Sprintf("%s (%s) on %s", m.Author.Username, m.Author.ID, m.GuildID), "User: "+sanitizedUserPrompt)
	if err != nil {
		warnErr := fmt.Errorf("failed to record conversation message: %w", err)
		span.RecordError(warnErr)
		logger.WarnContext(ctx, "non-fatal error updating thread context", slog.Any("error", warnErr))
	}

	err = b.addThreadMessage(ctx, responseChannel, "Bot", responseText)
	if err != nil {
		warnErr := fmt.Errorf("failed to record conversation message: %w", err)
		span.RecordError(warnErr)
		logger.WarnContext(ctx, "non-fatal error updating thread context", slog.Any("error", warnErr))
	}

	err = b.sendMessage(ctx, responseChannel, responseText)
	if err != nil {
		return fmt.Errorf("failed to respond to discord channel: %w", err)
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

// handleFatalOpenAIError logs the terminal OpenAI failure with enough detail
// to identify the cause in CloudWatch (quota vs revoked key), then triggers a
// graceful process shutdown so main.go can close the Discord session cleanly.
// Repeated calls are safe — context.CancelFunc is idempotent.
func (b *AIBot) handleFatalOpenAIError(ctx context.Context, err error) {
	logger := slog.Default().WithGroup("openai")
	var apiErr *openai.Error
	attrs := []any{slog.Any("error", err)}
	if errors.As(err, &apiErr) {
		attrs = append(attrs,
			slog.Int("http_status", apiErr.StatusCode),
			slog.String("openai_message", apiErr.Message),
			slog.String("openai_error_code", apiErr.Code),
		)
	}
	logger.ErrorContext(ctx, "Unrecoverable OpenAI error; triggering shutdown", attrs...)
	if b.shutdown != nil {
		b.shutdown()
	}
}

func (b *AIBot) Shutdown() {
	// The bot context may already be cancelled during shutdown; the goodbye
	// message still gets its own Discord deadline.
	err := b.sendMessage(context.WithoutCancel(b.botCtx), "1091532074495787049", "Here I go, shutting down again!")
	_, span := otel.GetTracerProvider().Tracer("AIBot").Start(b.botCtx, "Shutdown")
	span.RecordError(err)
	span.End()
}
