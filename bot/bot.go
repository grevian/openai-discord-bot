package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/avast/retry-go/v4"
	"github.com/bwmarrin/discordgo"
	"github.com/pkg/errors"
	gpt "github.com/sashabaranov/go-openai"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.uber.org/zap"

	"openai-discord-bot/bot/storage"
)

type AIBot struct {
	logger         *zap.Logger
	openapiClient  *gpt.Client
	botCtx         context.Context
	discordSession *discordgo.Session
	basePrompt     []gpt.ChatCompletionMessage
	storage        *storage.Storage
	imageStorage   *storage.ImageStorage
}

func (b *AIBot) Go() error {
	// TODO Block here? Use a context or a control channel?
	return nil
}

func NewAIBot(botCtx context.Context, aiClient *gpt.Client, discordSession *discordgo.Session, storage *storage.Storage, imageStorage *storage.ImageStorage, logger *zap.Logger) *AIBot {
	promptBytes, err := os.ReadFile("prompts/danbo.json")
	if err != nil {
		logger.Panic("Failed to read initial prompt", zap.Error(err))
	}

	promptMessages := struct {
		Prompt []gpt.ChatCompletionMessage
	}{}
	err = json.Unmarshal(promptBytes, &promptMessages)

	if err != nil {
		logger.Panic("Failed to parse initial prompt", zap.Error(err))
	}

	bot := &AIBot{
		discordSession: discordSession,
		logger:         logger,
		openapiClient:  aiClient,
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

	for u := range mentioned {
		if user.ID == mentioned[u].ID {
			return true
		}
	}

	return false
}

func (b *AIBot) messageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
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

	b.logger.Info("Processing Message", zap.String("message", m.Content))

	// Figure out if we should be acting in a thread
	responseChannel, threadPromptContext, err := b.handleThreading(ctx, s, m)
	if err != nil {
		b.logger.Error("failed to load or create thread context", zap.Error(err))
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
		return
	}
	b.logger.Debug("loaded thread context", zap.Int("thread_length", len(threadPromptContext)))

	// Strip our UserId out of messages to keep the record from being too confusing,
	sanitizedUserPrompt := strings.ReplaceAll(m.Content, fmt.Sprintf("<@%s>", s.State.User.ID), "")

	// Let users know we're "typing", the call to OpenAI can take a few seconds
	_ = s.ChannelTyping(responseChannel, discordgo.WithContext(ctx))

	if strings.Contains(strings.ToLower(sanitizedUserPrompt), "🎨") || strings.Contains(strings.ToLower(sanitizedUserPrompt), "draw me a picture of") {
		// Strip the prompt prefix out of the message
		sanitizedUserPrompt = strings.ReplaceAll(strings.ToLower(m.Content), "draw me a picture of", "")
		err = b.handleImageMessage(ctx, responseChannel, sanitizedUserPrompt, m)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			_, discordErr := b.discordSession.ChannelMessageSend(responseChannel, fmt.Sprintf("I fucked that up and threw it away. Sorry. (%s)", err.Error()), discordgo.WithContext(ctx))
			if discordErr != nil {
				b.logger.Error("Failed to notify discord channel of the error", zap.Error(err))
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
				b.logger.Error("Failed to notify discord channel of the error", zap.Error(err))
			}
			return
		}
	}
	span.SetStatus(codes.Ok, "Success")
}

func (b *AIBot) ReadyHandler(_ *discordgo.Session, _ *discordgo.Ready) {
	b.logger.Info("Connection state ready, Registering intents")

	b.logger.Info("Ready!")
}

func (b *AIBot) handleImageMessage(ctx context.Context, responseChannel string, prompt string, m *discordgo.MessageCreate) error {
	var err error

	// Record the prompt to our thread context
	err = b.storage.AddThreadMessage(ctx, responseChannel, fmt.Sprintf("%s (%s) on %s", m.Author.Username, m.Author.ID, m.GuildID), prompt)
	if err != nil {
		return fmt.Errorf("failed to record drawing prompt: %w", err)
	}

	// Request the image(s) from openAI
	imageRequest := gpt.ImageRequest{
		Prompt:         prompt,
		N:              1,
		Size:           "512x512",
		ResponseFormat: "url",
		User:           m.Author.ID,
		Size:           gpt.CreateImageSize1024x1024,
		ResponseFormat: gpt.CreateImageResponseFormatURL,
		Model:          gpt.CreateImageModelDallE3,
	}
	responseImage, err := b.openapiClient.CreateImage(ctx, imageRequest)
	if err != nil {
		return fmt.Errorf("failed to get image from openai: %w", err)
	}

	// Retrieve the image from openai
	imageReader, imageLength, err := b.imageStorage.GetImageFromURL(ctx, responseImage.Data[0].URL)
	if err != nil {
		return fmt.Errorf("failed to store retrieved image: %w", err)
	}
	b.logger.Debug("image retrieval", zap.Int64("image_length", imageLength), zap.String("url", responseImage.Data[0].URL))

	defer func() {
		closeErr := imageReader.Close()
		if closeErr != nil {
			b.logger.Error("failed to close image request body", zap.Error(closeErr))
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
				b.logger.Error("failed to close the pipeWriter", zap.Error(pipeErr))
			}
		}()

		imageKey, err := b.imageStorage.StoreImage(ctx, m.GuildID, imageTeeReader, imageLength)
		if err != nil {
			b.logger.Error("failed to store a copy of the image in S3", zap.Error(err))
			return
		}

		imageUrl := fmt.Sprintf("https://sillybullshit.click/%s", imageKey)

		// Record the image response to the thread context
		err = b.storage.AddThreadMessage(ctx, responseChannel, "Bot", imageUrl)
		if err != nil {
			b.logger.Error("failed to store a copy of the image in S3", zap.Error(err))
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

	return nil
}

// Handle a text completion prompt, including applying existing thread context and updating the stored state of that context
func (b *AIBot) handleCompletionPrompt(ctx context.Context, responseChannel string, sanitizedUserPrompt string, threadPromptContext []gpt.ChatCompletionMessage, m *discordgo.MessageCreate) error {
	var err error

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
				b.logger.Error("Failed to retrieve completion from OpenAI", zap.Error(err))
				return err
			}
			responseText = response.Choices[0].Message.Content
			if responseText == "" {
				b.logger.Warn("Empty response text from OpenAI", zap.Reflect("response", response))
				return errors.New("Received an empty response from OpenAI")
			}
			return nil
		},
		retry.Attempts(3),
	)
	if err != nil {
		return fmt.Errorf("failed to get response from openai: %w", err)
	}

	// TODO It's weird that we're modifying the stored thread state here, but loaded it elsewhere
	err = b.storage.AddThreadMessage(ctx, responseChannel, fmt.Sprintf("%s (%s) on %s", m.Author.Username, m.Author.ID, m.GuildID), "User: "+userMessage.Content)
	if err != nil {
		// TODO Return this error, or inject the tracing span, to ensure its surfaced in the span
		// TODO Record the messageSource too
		warnErr := fmt.Errorf("failed to record conversation message: %w", err)
		b.logger.Warn("non-fatal error updating thread context", zap.Error(warnErr))
	}

	err = b.storage.AddThreadMessage(ctx, responseChannel, "Bot", responseText)
	if err != nil {
		// TODO Return this error, or inject the tracing span, to ensure its surfaced in the span
		// TODO Record the messageSource too
		warnErr := fmt.Errorf("failed to record conversation message: %w", err)
		b.logger.Warn("non-fatal error updating thread context", zap.Error(warnErr))
	}

	_, err = b.discordSession.ChannelMessageSend(responseChannel, responseText, discordgo.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("failed to respond to discord channel: %w", err)
	}

	return nil
}

// Create a new thread if requested, or load the context of a thread if already in one
func (b *AIBot) handleThreading(ctx context.Context, s *discordgo.Session, m *discordgo.MessageCreate) (responseChannel string, threadContext []gpt.ChatCompletionMessage, errResponse error) {
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
			b.logger.Warn("Failed to load thread conversation context", zap.Error(warnErr), zap.String("thread_id", responseChannel))
		}
	}
	return
}

func (b *AIBot) Shutdown() {
	b.discordSession.ChannelMessageSend("", "Here I go, shutting down again!")

}
