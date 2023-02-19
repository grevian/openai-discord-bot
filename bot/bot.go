package bot

import (
	"context"
	"fmt"
	"os"

	"github.com/avast/retry-go"
	"github.com/bwmarrin/discordgo"
	"github.com/pkg/errors"
	gpt "github.com/sashabaranov/go-gpt3"
	"go.uber.org/zap"
)

type AIBot struct {
	logger         *zap.Logger
	openapiClient  *gpt.Client
	botCtx         context.Context
	discordSession *discordgo.Session
	basePrompt     string
}

func (b *AIBot) Go() error {
	// TODO Block here? Use a context or a control channel?
	err := b.discordSession.Open()
	if err != nil {
		return err
	}
	return nil
}

func NewAIBot(botCtx context.Context, aiClient *gpt.Client, discordSession *discordgo.Session, logger *zap.Logger) *AIBot {
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

	b.logger.Info("Processing Message", zap.String("message", m.Content))

	request := gpt.CompletionRequest{
		Model:       gpt.GPT3TextDavinci003,
		Prompt:      fmt.Sprintf("%s \n %s", b.basePrompt, m.Content),
		MaxTokens:   500,
		Temperature: 0.6,
	}

	var responseText string
	err := retry.Do(
		func() error {
			response, err := b.openapiClient.CreateCompletion(context.Background(), request)
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
		_, discordErr := b.discordSession.ChannelMessageSend(m.ChannelID, "Whoops something went wrong processing that")
		if discordErr != nil {
			b.logger.Error("Failed to notify discord channel of the error", zap.Error(err))
		}
		return
	}

	_, err = b.discordSession.ChannelMessageSend(m.ChannelID, responseText)
	if err != nil {
		b.logger.Error("Failed to respond to discord channel", zap.Error(err))
	}
}

func (b *AIBot) ReadyHandler(session *discordgo.Session, ready *discordgo.Ready) {
	b.logger.Info("Connection state ready, Registering intents")

	b.logger.Info("Ready!")
}
