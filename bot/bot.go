package bot

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/avast/retry-go"
	"github.com/bwmarrin/discordgo"
	"github.com/pkg/errors"
	gpt "github.com/sashabaranov/go-gpt3"
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
	basePrompt     string
	storage        *storage.Storage
}

func (b *AIBot) Go() error {
	// TODO Block here? Use a context or a control channel?
	err := b.discordSession.Open()
	if err != nil {
		return err
	}
	return nil
}

func NewAIBot(botCtx context.Context, aiClient *gpt.Client, discordSession *discordgo.Session, storage *storage.Storage, logger *zap.Logger) *AIBot {
	promptBytes, err := os.ReadFile("prompts/danbo.txt")
	if err != nil {
		logger.Panic("Failed to read initial prompt", zap.Error(err))
	}

	bot := &AIBot{
		discordSession: discordSession,
		logger:         logger,
		openapiClient:  aiClient,
		botCtx:         botCtx,
		basePrompt:     string(promptBytes),
		storage:        storage,
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
	responseChannel := m.ChannelID
	wantThreaded := strings.Contains(m.Message.Content, "🧵")

	isThreaded := false
	if ch, err := s.State.Channel(m.ChannelID); err == nil && ch.IsThread() { // The "channel" exists and is a thread
		isThreaded = true
		responseChannel = ch.ID
	}

	// If the user requested a thread but we're not in one yet, create it
	if wantThreaded && !isThreaded {
		ch, err := s.MessageThreadStartComplex(m.ChannelID, m.ID, &discordgo.ThreadStart{
			Name:                fmt.Sprintf("Conversation with %s", m.Message.Author),
			AutoArchiveDuration: 60,
		}, discordgo.WithContext(ctx))
		if err != nil {
			b.logger.Error("Failed to create discord conversation thread", zap.Error(err))
		}
		responseChannel = ch.ID
		isThreaded = true
	}

	// If we are in a thread, we should load the thread's conversation context
	threadPromptContext, err := b.storage.GetThread(ctx, responseChannel)
	if err != nil {
		// This doesn't have to be fatal, though it may be confusing
		b.logger.Error("Failed to load thread conversation context", zap.Error(err), zap.String("thread_id", responseChannel))
	}

	request := gpt.CompletionRequest{
		Model:       gpt.GPT3TextDavinci003,
		Prompt:      fmt.Sprintf("%s \n%s \n%s \nDanbot:", b.basePrompt, threadPromptContext, m.Content),
		MaxTokens:   500,
		Temperature: 0.6,
	}

	var responseText string
	err = retry.Do(
		func() error {
			response, err := b.openapiClient.CreateCompletion(ctx, request)
			if err != nil {
				b.logger.Error("Failed to retrieve completion from OpenAI", zap.Error(err))
				return err
			}
			responseText = response.Choices[0].Text
			if responseText == "" {
				b.logger.Warn("Empty response text from OpenAI", zap.Reflect("response", response))
				return errors.New("Received an empty response from OpenAI")
			}
			return nil
		},
		retry.Attempts(3),
	)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		_, discordErr := b.discordSession.ChannelMessageSend(responseChannel, "Whoops something went wrong processing that", discordgo.WithContext(ctx))
		if discordErr != nil {
			b.logger.Error("Failed to notify discord channel of the error", zap.Error(err))
		}
		return
	}

	// If we're in a thread, record the new prompt text and the response, to continue the context for the next message
	if isThreaded {
		err = b.storage.AddThreadMessage(ctx, responseChannel, "User: "+m.Content)
		if err != nil {
			b.logger.Error("Failed to record conversation message", zap.Error(err), zap.String("source", "message"))
		}
		err = b.storage.AddThreadMessage(ctx, responseChannel, "Danbot: "+responseText)
		if err != nil {
			b.logger.Error("Failed to record conversation message", zap.Error(err), zap.String("source", "openai"))
		}
	}

	_, err = b.discordSession.ChannelMessageSend(responseChannel, responseText, discordgo.WithContext(ctx))
	if err != nil {
		b.logger.Error("Failed to respond to discord channel", zap.Error(err))
	}
	span.SetStatus(codes.Ok, "Success")
}

func (b *AIBot) ReadyHandler(session *discordgo.Session, ready *discordgo.Ready) {
	b.logger.Info("Connection state ready, Registering intents")

	b.logger.Info("Ready!")
}
