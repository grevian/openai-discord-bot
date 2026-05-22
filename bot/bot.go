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
	"strings"

	"openai-discord-bot/bot/storage"

	"github.com/avast/retry-go/v4"
	"github.com/bwmarrin/discordgo"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type AIBot struct {
	openapiClient       openai.Client
	botCtx              context.Context
	shutdown            context.CancelFunc
	discordSession      *discordgo.Session
	systemInstructions  string
	drawingInstructions string
	basePrompt          []storage.ConversationMessage
	storage             *storage.Storage
	imageStorage        *storage.ImageStorage
}

func (b *AIBot) Go() error {
	// TODO Block here? Use a context or a control channel?
	return nil
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
	promptBytes, err := os.ReadFile("prompts/danbo.json")
	if err != nil {
		log.Panic("Failed to read initial prompt", err)
	}

	promptFile := struct {
		Instructions        string                        `json:"instructions"`
		DrawingInstructions string                        `json:"drawing_instructions"`
		Examples            []storage.ConversationMessage `json:"examples"`
	}{}
	err = json.Unmarshal(promptBytes, &promptFile)

	if err != nil {
		log.Panic("Failed to parse initial prompt", err)
	}

	bot := &AIBot{
		discordSession:      discordSession,
		openapiClient:       aiClient,
		botCtx:              botCtx,
		shutdown:            shutdown,
		systemInstructions:  promptFile.Instructions,
		drawingInstructions: promptFile.DrawingInstructions,
		basePrompt:          promptFile.Examples,
		storage:             threadStorage,
		imageStorage:        imageStorage,
	}

	// TODO Wire up more handlers
	discordSession.AddHandler(bot.ReadyHandler)
	discordSession.AddHandler(bot.messageCreate)

	return bot
}

// decodedBase64Length returns the number of bytes a standard-base64 string will
// decode to, without performing the decode. S3 needs the content length up
// front, and we want to avoid buffering the decoded image just to measure it.
func decodedBase64Length(s string) (int64, error) {
	if len(s)%4 != 0 {
		return 0, fmt.Errorf("base64 length %d is not a multiple of 4", len(s))
	}
	padding := 0
	switch {
	case strings.HasSuffix(s, "=="):
		padding = 2
	case strings.HasSuffix(s, "="):
		padding = 1
	}
	return int64(len(s)/4*3 - padding), nil
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

func (b *AIBot) messageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	logger := slog.Default().WithGroup("messageCreate")
	if m.Author.ID == s.State.User.ID {
		return
	}

	if !userWasMentioned(s.State.User, m.Mentions) {
		return
	}

	ctx, span := otel.GetTracerProvider().Tracer("AIBot").Start(context.Background(), "messageCreate")
	span.SetAttributes(
		attribute.String("user", m.Author.ID),
		attribute.String("guild", m.GuildID),
		attribute.String("channel", m.ChannelID),
	)
	defer span.End()

	logger.InfoContext(ctx, "Processing Message", slog.String("message", m.Content))

	// Figure out if we should be acting in a thread
	responseChannel, threadPromptContext, err := b.handleThreading(ctx, s, m)
	if err != nil {
		logger.ErrorContext(ctx, "failed to load or create thread context", slog.Any("error", err))
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
		return
	}
	logger.DebugContext(ctx, "loaded thread context", slog.Int("thread_length", len(threadPromptContext)))

	// Strip our UserId out of messages to keep the record from being too confusing,
	sanitizedUserPrompt := strings.ReplaceAll(m.Content, fmt.Sprintf("<@%s>", s.State.User.ID), "")

	// Let users know we're "typing", the call to OpenAI can take a few seconds
	_ = s.ChannelTyping(responseChannel, discordgo.WithContext(ctx))

	sourceImageURL := findEditSourceImage(s, m)
	lowerPrompt := strings.ToLower(sanitizedUserPrompt)
	wantsEdit := sourceImageURL != "" && (strings.Contains(sanitizedUserPrompt, "✏️") ||
		strings.Contains(lowerPrompt, "edit:") ||
		strings.Contains(lowerPrompt, "redraw"))
	wantsDraw := strings.Contains(lowerPrompt, "🎨") || strings.Contains(lowerPrompt, "draw me a picture of")

	switch {
	case wantsEdit:
		editPrompt := stripEditTriggers(sanitizedUserPrompt)
		err = b.handleImageEditMessage(ctx, responseChannel, editPrompt, sourceImageURL, m)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			_, discordErr := b.discordSession.ChannelMessageSend(responseChannel, fmt.Sprintf("I fucked that up and threw it away. Sorry. (%s)", err.Error()), discordgo.WithContext(ctx))
			if discordErr != nil {
				span.RecordError(err)
				logger.ErrorContext(ctx, "Failed to notify discord channel of the error", slog.Any("error", err))
			}
			return
		}
	case wantsDraw:
		// Strip the prompt prefix out of the message
		sanitizedUserPrompt = strings.ReplaceAll(strings.ToLower(m.Content), "draw me a picture of", "")
		err = b.handleImageMessage(ctx, responseChannel, sanitizedUserPrompt, m)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			_, discordErr := b.discordSession.ChannelMessageSend(responseChannel, fmt.Sprintf("I fucked that up and threw it away. Sorry. (%s)", err.Error()), discordgo.WithContext(ctx))
			if discordErr != nil {
				span.RecordError(err)
				logger.ErrorContext(ctx, "Failed to notify discord channel of the error", slog.Any("error", err))
			}
			return
		}
	default:
		err = b.handleCompletionPrompt(ctx, responseChannel, sanitizedUserPrompt, threadPromptContext, m)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			_, discordErr := b.discordSession.ChannelMessageSend(responseChannel, "Whoops something went wrong processing that", discordgo.WithContext(ctx))
			if discordErr != nil {
				span.RecordError(err)
				logger.ErrorContext(ctx, "Failed to notify discord channel of the error", slog.Any("error", err))
			}
			return
		}
	}
	span.SetStatus(codes.Ok, "Success")
}

func (b *AIBot) ReadyHandler(_ *discordgo.Session, _ *discordgo.Ready) {
	slog.Default().WithGroup("ReadyHandler").Info("Connection state ready, Registering intents")
	_, err := b.discordSession.ChannelMessageSend("1091532074495787049", "Gah! I'm awake! How long was I out?!")
	if err != nil {
		slog.Default().Error("Failed to notify operator of the ready event", slog.Any("error", err))
	}
}

func (b *AIBot) handleImageMessage(ctx context.Context, responseChannel string, prompt string, m *discordgo.MessageCreate) error {
	var err error

	ctx, span := otel.GetTracerProvider().Tracer("AIBot").Start(ctx, "handleImageMessage")
	defer span.End()

	// Record the prompt to our thread context
	err = b.storage.AddThreadMessage(ctx, responseChannel, fmt.Sprintf("%s (%s) on %s", m.Author.Username, m.Author.ID, m.GuildID), prompt)
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
		Prompt: fullPrompt,
		N:      openai.Int(1),
		User:   openai.String(m.Author.ID),
		Size:   openai.ImageGenerateParamsSize1024x1024,
		Model:  openai.ImageModelGPTImage2,
	}
	span.SetAttributes(
		attribute.String("model", string(imageRequest.Model)),
		attribute.String("size", string(imageRequest.Size)),
	)
	responseImage, err := b.openapiClient.Images.Generate(ctx, imageRequest)
	if err != nil {
		if isFatalOpenAIError(err) {
			b.handleFatalOpenAIError(ctx, err)
		}
		return fmt.Errorf("failed to get image from openai: %w", err)
	}

	if err := b.dispatchGeneratedImage(ctx, span, responseChannel, responseImage.Data[0].B64JSON, "a picture I drawed", m); err != nil {
		return err
	}

	span.SetStatus(codes.Ok, "Success")
	return nil
}

// dispatchGeneratedImage takes a base64 PNG from the OpenAI image API, streams
// it to Discord as a reply attachment, and concurrently uploads a copy to S3 +
// records the resulting URL in thread context. Shared between the generation
// and edit paths.
func (b *AIBot) dispatchGeneratedImage(ctx context.Context, span trace.Span, responseChannel, b64, content string, m *discordgo.MessageCreate) error {
	logger := slog.Default().WithGroup("dispatchGeneratedImage")

	imageLength, err := decodedBase64Length(b64)
	if err != nil {
		return fmt.Errorf("invalid base64 image from openai: %w", err)
	}
	logger.DebugContext(ctx, "image retrieval", slog.Int64("image_length", imageLength))

	// Tee the image reading stream, so that we can upload it to discord and S3 at the same time
	pipeReader, pipeWriter := io.Pipe()
	imageDecoder := base64.NewDecoder(base64.StdEncoding, strings.NewReader(b64))
	imageTeeReader := io.TeeReader(imageDecoder, pipeWriter)

	go func() {
		defer func() {
			pipeErr := pipeWriter.Close()
			if pipeErr != nil {
				span.RecordError(pipeErr)
				logger.ErrorContext(ctx, "failed to close the pipeWriter", slog.Any("error", pipeErr))
			}
		}()

		imageKey, err := b.imageStorage.StoreImage(ctx, m.GuildID, imageTeeReader, imageLength)
		if err != nil {
			span.RecordError(err)
			logger.ErrorContext(ctx, "failed to store a copy of the image in S3", slog.Any("error", err))
			return
		}

		imageUrl := fmt.Sprintf("https://sillybullshit.click/%s", imageKey)

		if err := b.storage.AddThreadMessage(ctx, responseChannel, "Bot", imageUrl); err != nil {
			span.RecordError(err)
			logger.ErrorContext(ctx, "failed to record image URL in thread context", slog.Any("error", err))
		}
	}()

	_, err = b.discordSession.ChannelMessageSendComplex(responseChannel, &discordgo.MessageSend{
		Content:   content,
		Reference: m.Reference(),
		File: &discordgo.File{
			Name:        "danbot-drawing.png",
			ContentType: "image/png",
			Reader:      pipeReader,
		},
	}, discordgo.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("failed to send embedded image to discord: %w", err)
	}
	return nil
}

// handleImageEditMessage takes a prompt and a URL to an existing image and asks
// OpenAI's image edit endpoint to produce a modified version, then ships it the
// same way handleImageMessage does for fresh generations.
func (b *AIBot) handleImageEditMessage(ctx context.Context, responseChannel, prompt, sourceURL string, m *discordgo.MessageCreate) error {
	logger := slog.Default().WithGroup("handleImageEditMessage")

	ctx, span := otel.GetTracerProvider().Tracer("AIBot").Start(ctx, "handleImageEditMessage")
	defer span.End()
	span.SetAttributes(attribute.String("source_url", sourceURL))

	if err := b.storage.AddThreadMessage(ctx, responseChannel,
		fmt.Sprintf("%s (%s) on %s", m.Author.Username, m.Author.ID, m.GuildID),
		fmt.Sprintf("edit: %s (source: %s)", prompt, sourceURL)); err != nil {
		return fmt.Errorf("failed to record edit prompt: %w", err)
	}

	src, _, err := b.imageStorage.GetImageFromURL(ctx, sourceURL)
	if err != nil {
		return fmt.Errorf("failed to fetch source image: %w", err)
	}
	defer src.Close()

	// The Edit endpoint sends the image as multipart form data, which means the
	// SDK reads the whole body up front — buffer it here so we can pass an
	// io.Reader that won't get drained partway through.
	srcBytes, err := io.ReadAll(src)
	if err != nil {
		return fmt.Errorf("failed to read source image: %w", err)
	}
	srcContentType, srcFilename := detectImageType(srcBytes)
	logger.DebugContext(ctx, "source image fetched",
		slog.Int("bytes", len(srcBytes)),
		slog.String("content_type", srcContentType))
	if srcContentType == "" {
		return fmt.Errorf("source image is not a supported format (png/jpeg/webp)")
	}

	fullPrompt := prompt
	if b.drawingInstructions != "" {
		fullPrompt = b.drawingInstructions + " " + prompt
	}

	// The OpenAI multipart encoder needs a recognizable filename + content type;
	// without it the form part is sent as application/octet-stream and the Edit
	// endpoint rejects it. openai.File wires both onto the io.Reader.
	editRequest := openai.ImageEditParams{
		Image:  openai.ImageEditParamsImageUnion{OfFile: openai.File(bytes.NewReader(srcBytes), srcFilename, srcContentType)},
		Prompt: fullPrompt,
		N:      openai.Int(1),
		User:   openai.String(m.Author.ID),
		Size:   openai.ImageEditParamsSize1024x1024,
		Model:  openai.ImageModelGPTImage1,
	}
	span.SetAttributes(
		attribute.String("model", string(editRequest.Model)),
		attribute.String("size", string(editRequest.Size)),
	)

	responseImage, err := b.openapiClient.Images.Edit(ctx, editRequest)
	if err != nil {
		if isFatalOpenAIError(err) {
			b.handleFatalOpenAIError(ctx, err)
		}
		return fmt.Errorf("failed to edit image via openai: %w", err)
	}

	if err := b.dispatchGeneratedImage(ctx, span, responseChannel, responseImage.Data[0].B64JSON, "here ya go", m); err != nil {
		return err
	}

	span.SetStatus(codes.Ok, "Success")
	return nil
}

// findEditSourceImage returns a URL to an image to be used as the source for an
// edit, preferring an attachment on the triggering message, then falling back
// to an attachment on the message it's replying to (if that message came from
// us). Returns "" when no candidate exists.
func findEditSourceImage(s *discordgo.Session, m *discordgo.MessageCreate) string {
	for _, att := range m.Attachments {
		if isImageAttachment(att) {
			return att.URL
		}
	}

	ref := m.ReferencedMessage
	if ref == nil && m.MessageReference != nil && m.MessageReference.MessageID != "" {
		// The gateway doesn't always inline ReferencedMessage — older messages
		// or cache misses fall back to an explicit fetch.
		fetched, err := s.ChannelMessage(m.MessageReference.ChannelID, m.MessageReference.MessageID)
		if err == nil {
			ref = fetched
		}
	}
	if ref == nil || ref.Author == nil || ref.Author.ID != s.State.User.ID {
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
		Model:        openai.ChatModelGPT4oMini,
		Instructions: openai.String(b.systemInstructions),
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: inputItems,
		},
	}

	// Text completions seem to fail shockingly often, so we set them up to retry if necessary
	var responseText string
	err = retry.Do(
		func() error {
			response, err := b.openapiClient.Responses.New(ctx, request)
			if err != nil {
				if isFatalOpenAIError(err) {
					b.handleFatalOpenAIError(ctx, err)
					return retry.Unrecoverable(err)
				}
				logger.ErrorContext(ctx, "Failed to retrieve completion from OpenAI", slog.Any("error", err))
				return err
			}
			responseText = response.OutputText()
			if responseText == "" {
				logger.WarnContext(ctx, "Empty response text from OpenAI", slog.Any("response", response))
				return errors.New("Received an empty response from OpenAI")
			}
			return nil
		},
		retry.Attempts(3),
		retry.OnRetry(func(n uint, err error) {
			span.AddEvent("retry creating chat completion", trace.WithAttributes(attribute.Int("retry", int(n))))
			span.RecordError(err)
		}),
	)
	if err != nil {
		return fmt.Errorf("failed to get response from openai: %w", err)
	}

	// TODO It's weird that we're modifying the stored thread state here, but loaded it elsewhere
	err = b.storage.AddThreadMessage(ctx, responseChannel, fmt.Sprintf("%s (%s) on %s", m.Author.Username, m.Author.ID, m.GuildID), "User: "+sanitizedUserPrompt)
	if err != nil {
		warnErr := fmt.Errorf("failed to record conversation message: %w", err)
		span.RecordError(warnErr)
		logger.WarnContext(ctx, "non-fatal error updating thread context", slog.Any("error", warnErr))
	}

	err = b.storage.AddThreadMessage(ctx, responseChannel, "Bot", responseText)
	if err != nil {
		warnErr := fmt.Errorf("failed to record conversation message: %w", err)
		span.RecordError(warnErr)
		logger.WarnContext(ctx, "non-fatal error updating thread context", slog.Any("error", warnErr))
	}

	_, err = b.discordSession.ChannelMessageSend(responseChannel, responseText, discordgo.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("failed to respond to discord channel: %w", err)
	}

	span.SetStatus(codes.Ok, "Success")
	return nil
}

// Create a new thread if requested, or load the context of a thread if already in one
func (b *AIBot) handleThreading(ctx context.Context, s *discordgo.Session, m *discordgo.MessageCreate) (responseChannel string, threadContext []storage.ConversationMessage, errResponse error) {
	logger := slog.Default().WithGroup("handleThreading")
	// Default to responding to the channel the message came from
	responseChannel = m.ChannelID
	isThreaded := false
	wantThreaded := strings.Contains(m.Message.Content, "🧵")

	// The current "channel" may already be a thread
	if ch, err := s.State.Channel(m.ChannelID); err == nil && ch.IsThread() {
		isThreaded = true
		responseChannel = ch.ID
	}

	// But if the user requested a thread, but we're not in one yet, create it
	if wantThreaded && !isThreaded {
		ch, err := s.MessageThreadStartComplex(m.ChannelID, m.ID, &discordgo.ThreadStart{
			Name:                fmt.Sprintf("Conversation with %s", m.Message.Author.Username),
			AutoArchiveDuration: 60,
		}, discordgo.WithContext(ctx))
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
		threadContext, err = b.storage.GetThread(ctx, responseChannel)
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
	_, err := b.discordSession.ChannelMessageSend("1091532074495787049", "Here I go, shutting down again!")
	_, span := otel.GetTracerProvider().Tracer("AIBot").Start(b.botCtx, "Shutdown")
	span.RecordError(err)
	span.End()
}
