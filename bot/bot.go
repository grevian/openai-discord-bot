package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"slices"
	"strings"

	"openai-discord-bot/bot/storage"

	"github.com/avast/retry-go/v4"
	"github.com/bwmarrin/discordgo"
	"github.com/pkg/errors"
	gpt "github.com/sashabaranov/go-openai"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type aiClient interface {
	CreateImage(ctx context.Context, request gpt.ImageRequest) (gpt.ImageResponse, error)
	CreateEditImage(ctx context.Context, request gpt.ImageEditRequest) (gpt.ImageResponse, error)
	CreateChatCompletion(ctx context.Context, request gpt.ChatCompletionRequest) (gpt.ChatCompletionResponse, error)
}

type threadStore interface {
	GetThread(ctx context.Context, threadId string) ([]gpt.ChatCompletionMessage, error)
	AddThreadMessage(ctx context.Context, threadId string, messageSource string, message string) error
}

type imageStore interface {
	GetImageFromURL(ctx context.Context, URL string) (io.ReadCloser, int64, error)
	StoreImage(ctx context.Context, groupId string, reader io.Reader, contentLength int64) (string, error)
}

type AIBot struct {
	openapiClient  aiClient
	botCtx         context.Context
	discordSession *discordgo.Session
	basePrompt     []gpt.ChatCompletionMessage
	storage        threadStore
	imageStorage   imageStore
}

func (b *AIBot) Go() error {
	// TODO Block here? Use a context or a control channel?
	return nil
}

func NewAIBot(botCtx context.Context, aiClient *gpt.Client, discordSession *discordgo.Session, storage *storage.Storage, imageStorage *storage.ImageStorage) *AIBot {
	promptBytes, err := os.ReadFile("prompts/danbo.json")
	if err != nil {
		log.Panic("Failed to read initial prompt", err)
	}

	promptMessages := struct {
		Prompt []gpt.ChatCompletionMessage
	}{}
	err = json.Unmarshal(promptBytes, &promptMessages)

	if err != nil {
		log.Panic("Failed to parse initial prompt", err)
	}

	bot := &AIBot{
		discordSession: discordSession,
		openapiClient:  newOpenaiClientWrapper(aiClient),
		botCtx:         botCtx,
		basePrompt:     promptMessages.Prompt,
		storage:        storage,
		imageStorage:   imageStorage,
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
	return slices.ContainsFunc(mentioned, func(u *discordgo.User) bool {
		return user.ID == u.ID
	})
}

func (b *AIBot) messageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	logger := slog.Default().WithGroup("messageCreate")
	if m.Author.ID == s.State.User.ID {
		return
	}

	if !userWasMentioned(s.State.User, m.Mentions) {
		return
	}

	ctx, span := otel.GetTracerProvider().Tracer("AIBot").Start(b.botCtx, "messageCreate")
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

	sanitizedLower := strings.ToLower(sanitizedUserPrompt)
	if strings.Contains(sanitizedLower, "✏️") {
		prompt := strings.TrimSpace(strings.ReplaceAll(sanitizedLower, "✏️", ""))
		err = b.handleImageEditMessage(ctx, responseChannel, prompt, threadPromptContext, m)
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
	} else if strings.Contains(sanitizedLower, "🎨") || strings.Contains(sanitizedLower, "draw me a picture of") {
		// Strip the prompt prefix out of the message
		prompt := strings.TrimSpace(strings.ReplaceAll(sanitizedLower, "draw me a picture of", ""))
		err = b.handleImageMessage(ctx, responseChannel, prompt, threadPromptContext, m)
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
	} else {
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
}

func (b *AIBot) handleImageMessage(ctx context.Context, responseChannel string, prompt string, threadPromptContext []gpt.ChatCompletionMessage, m *discordgo.MessageCreate) error {
	var err error
	logger := slog.Default().WithGroup("handleImageMessage")

	ctx, span := otel.GetTracerProvider().Tracer("AIBot").Start(ctx, "handleImageMessage")
	defer span.End()

	// If we're in a thread, combine the original prompt with the new instructions
	if len(threadPromptContext) > 0 {
		if originalPrompt := findOriginalPrompt(threadPromptContext); originalPrompt != "" {
			prompt = fmt.Sprintf("Original image prompt: %s. Modification: %s", originalPrompt, prompt)
		}
	}

	// Record the prompt to our thread context
	err = b.storage.AddThreadMessage(ctx, responseChannel, fmt.Sprintf("%s (%s) on %s", m.Author.Username, m.Author.ID, m.GuildID), prompt)
	if err != nil {
		return fmt.Errorf("failed to record drawing prompt: %w", err)
	}

	// Request the image(s) from openAI
	imageRequest := gpt.ImageRequest{
		Prompt:         prompt,
		N:              1,
		User:           m.Author.ID,
		Size:           gpt.CreateImageSize1024x1024,
		ResponseFormat: gpt.CreateImageResponseFormatURL,
		Model:          gpt.CreateImageModelDallE3,
	}
	span.SetAttributes(
		attribute.String("model", imageRequest.Model),
		attribute.String("size", imageRequest.Size),
	)
	responseImage, err := b.openapiClient.CreateImage(ctx, imageRequest)
	if err != nil {
		return fmt.Errorf("failed to get image from openai: %w", err)
	}

	// Retrieve the image from openai
	imageReader, imageLength, err := b.imageStorage.GetImageFromURL(ctx, responseImage.Data[0].URL)
	if err != nil {
		return fmt.Errorf("failed to store retrieved image: %w", err)
	}
	logger.DebugContext(ctx, "image retrieval", slog.Int64("image_length", imageLength), slog.String("url", responseImage.Data[0].URL))

	defer func() {
		closeErr := imageReader.Close()
		if closeErr != nil {
			span.RecordError(err)
			logger.ErrorContext(ctx, "failed to close image request body", slog.Any("error", closeErr))
		}
	}()

	// Tee the image reading stream, so that we can upload it to discord and S3 at the same time
	pipeReader, pipeWriter := io.Pipe()
	imageTeeReader := io.TeeReader(imageReader, pipeWriter)

	// Record the image to S3, and our thread context
	go func() {
		defer func() {
			pipeErr := pipeWriter.Close()
			if pipeErr != nil {
				span.RecordError(err)
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

		// Record the image response to the thread context
		err = b.storage.AddThreadMessage(ctx, responseChannel, "Bot", imageUrl)
		if err != nil {
			span.RecordError(err)
			logger.ErrorContext(ctx, "failed to store a copy of the image in S3", slog.Any("error", err))
		}
	}()

	// Embed the image in a discord message, and send it
	_, err = b.discordSession.ChannelMessageSendComplex(responseChannel, &discordgo.MessageSend{
		Content:   "a picture I drawed",
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

	span.SetStatus(codes.Ok, "Success")
	return nil
}

// Handle a text completion prompt, including applying existing thread context and updating the stored state of that context
func (b *AIBot) handleCompletionPrompt(ctx context.Context, responseChannel string, sanitizedUserPrompt string, threadPromptContext []gpt.ChatCompletionMessage, m *discordgo.MessageCreate) error {
	var err error
	logger := slog.Default().WithGroup("handleCompletionPrompt")
	ctx, span := otel.GetTracerProvider().Tracer("AIBot").Start(ctx, "handleCompletionPrompt")
	defer span.End()

	userMessage := gpt.ChatCompletionMessage{
		Role:    "user",
		Content: sanitizedUserPrompt,
	}
	requestMessages := append(threadPromptContext, userMessage)

	request := gpt.ChatCompletionRequest{
		Model:    gpt.GPT3Dot5Turbo,
		Messages: append(b.basePrompt, requestMessages...),
	}

	// Text completions seem to fail shockingly often, so we set them up to retry if necessary
	var responseText string
	err = retry.Do(
		func() error {
			response, err := b.openapiClient.CreateChatCompletion(ctx, request)
			if err != nil {
				logger.ErrorContext(ctx, "Failed to retrieve completion from OpenAI", slog.Any("error", err))
				return err
			}
			responseText = response.Choices[0].Message.Content
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
	err = b.storage.AddThreadMessage(ctx, responseChannel, fmt.Sprintf("%s (%s) on %s", m.Author.Username, m.Author.ID, m.GuildID), "User: "+userMessage.Content)
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
func (b *AIBot) handleThreading(ctx context.Context, s *discordgo.Session, m *discordgo.MessageCreate) (responseChannel string, threadContext []gpt.ChatCompletionMessage, errResponse error) {
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

// findLastImageURL searches thread context messages for the most recent image URL
func findLastImageURL(threadContext []gpt.ChatCompletionMessage) string {
	for i := len(threadContext) - 1; i >= 0; i-- {
		content := threadContext[i].Content
		if strings.HasPrefix(content, "https://sillybullshit.click/") {
			return content
		}
	}
	return ""
}

// findOriginalPrompt returns the first user message from thread context (the original image prompt)
func findOriginalPrompt(threadContext []gpt.ChatCompletionMessage) string {
	for _, msg := range threadContext {
		if msg.Role == "user" {
			return msg.Content
		}
	}
	return ""
}

func (b *AIBot) handleImageEditMessage(ctx context.Context, responseChannel string, prompt string, threadPromptContext []gpt.ChatCompletionMessage, m *discordgo.MessageCreate) error {
	logger := slog.Default().WithGroup("handleImageEditMessage")

	ctx, span := otel.GetTracerProvider().Tracer("AIBot").Start(ctx, "handleImageEditMessage")
	defer span.End()

	// Find the most recent image URL from thread context
	imageURL := findLastImageURL(threadPromptContext)
	if imageURL == "" {
		return fmt.Errorf("no image found in thread context to edit")
	}

	// Download the image
	imageReader, _, err := b.imageStorage.GetImageFromURL(ctx, imageURL)
	if err != nil {
		return fmt.Errorf("failed to download image for editing: %w", err)
	}
	defer imageReader.Close()

	// Record the prompt to our thread context
	err = b.storage.AddThreadMessage(ctx, responseChannel, fmt.Sprintf("%s (%s) on %s", m.Author.Username, m.Author.ID, m.GuildID), prompt)
	if err != nil {
		return fmt.Errorf("failed to record edit prompt: %w", err)
	}

	// Call DALL-E 2 edit API
	editRequest := gpt.ImageEditRequest{
		Image:          gpt.WrapReader(imageReader, "image.png", "image/png"),
		Prompt:         prompt,
		N:              1,
		Size:           gpt.CreateImageSize1024x1024,
		ResponseFormat: gpt.CreateImageResponseFormatURL,
		Model:          gpt.CreateImageModelDallE2,
	}

	responseImage, err := b.openapiClient.CreateEditImage(ctx, editRequest)
	if err != nil {
		return fmt.Errorf("failed to edit image via openai: %w", err)
	}

	// Download the edited image from OpenAI
	editedImageReader, editedImageLength, err := b.imageStorage.GetImageFromURL(ctx, responseImage.Data[0].URL)
	if err != nil {
		return fmt.Errorf("failed to retrieve edited image: %w", err)
	}
	defer editedImageReader.Close()

	logger.DebugContext(ctx, "edited image retrieval", slog.Int64("image_length", editedImageLength))

	// Tee the image for Discord + S3
	pipeReader, pipeWriter := io.Pipe()
	imageTeeReader := io.TeeReader(editedImageReader, pipeWriter)

	go func() {
		defer pipeWriter.Close()

		imageKey, storeErr := b.imageStorage.StoreImage(ctx, m.GuildID, imageTeeReader, editedImageLength)
		if storeErr != nil {
			span.RecordError(storeErr)
			logger.ErrorContext(ctx, "failed to store edited image in S3", slog.Any("error", storeErr))
			return
		}

		imageURL := fmt.Sprintf("https://sillybullshit.click/%s", imageKey)
		threadErr := b.storage.AddThreadMessage(ctx, responseChannel, "Bot", imageURL)
		if threadErr != nil {
			span.RecordError(threadErr)
			logger.ErrorContext(ctx, "failed to record edited image in thread context", slog.Any("error", threadErr))
		}
	}()

	_, err = b.discordSession.ChannelMessageSendComplex(responseChannel, &discordgo.MessageSend{
		Content:   "an edited picture I drawed",
		Reference: m.Reference(),
		File: &discordgo.File{
			Name:        "danbot-drawing.png",
			ContentType: "image/png",
			Reader:      pipeReader,
		},
	}, discordgo.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("failed to send edited image to discord: %w", err)
	}

	span.SetStatus(codes.Ok, "Success")
	return nil
}

func (b *AIBot) Shutdown() {
	_, err := b.discordSession.ChannelMessageSend("1091532074495787049", "Here I go, shutting down again!")
	_, span := otel.GetTracerProvider().Tracer("AIBot").Start(b.botCtx, "Shutdown")
	span.RecordError(err)
	span.End()
}
