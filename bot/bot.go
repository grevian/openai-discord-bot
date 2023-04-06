package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/avast/retry-go/v4"
	"github.com/bwmarrin/discordgo"
	"github.com/pkg/errors"
	gpt "github.com/sashabaranov/go-openai"
	"go.opentelemetry.io/otel"
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
	err := b.discordSession.Open()
	if err != nil {
		return err
	}
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

	// Strip our UserId out of messages to keep the record from being too confusing,
	sanitizedUserPrompt := strings.ReplaceAll(m.Content, fmt.Sprintf("<@%s>", s.State.User.ID), "")

	// Let users know we're "typing", the call to OpenAI can take a few seconds
	_ = s.ChannelTyping(responseChannel)

	if strings.Contains(strings.ToLower(sanitizedUserPrompt), "draw me a picture of") {
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
	}
	responseImage, err := b.openapiClient.CreateImage(ctx, imageRequest)
	if err != nil {
		return fmt.Errorf("failed to get image from openai: %w", err)
	}

	// Retrieve the image from openai and store it ourselves on S3
	imageKey, err := b.imageStorage.StoreImageFromURL(ctx, m.GuildID, responseImage.Data[0].URL)
	if err != nil {
		return fmt.Errorf("failed to store retrieved image: %w", err)
	}
	imageUrl := fmt.Sprintf("https://sillybullshit.click/%s", imageKey)

	// Record the image response to the thread context
	err = b.storage.AddThreadMessage(ctx, responseChannel, "Bot", responseImage.Data[0].URL)
	if err != nil {
		return fmt.Errorf("failed to record response to thread context: %w", err)
	}

	// Embed the image in a discord response, and send it
	_, err = b.discordSession.ChannelMessageSendEmbed(responseChannel, &discordgo.MessageEmbed{
		URL:         imageUrl,
		Type:        discordgo.EmbedTypeImage,
		Title:       "a picture I drawed",
		Description: m.Content,
		Timestamp:   time.Now().Format(time.RFC3339),
		Image: &discordgo.MessageEmbedImage{
			URL:    imageUrl,
			Width:  512,
			Height: 512,
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
