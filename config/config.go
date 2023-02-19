package config

import (
	"context"
	"fmt"
	"github.com/pkg/errors"
	"time"

	"github.com/bwmarrin/discordgo"
	gpt "github.com/sashabaranov/go-gpt3"
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

var logger *zap.Logger

func Configure() {
	viper.SetEnvPrefix("BOT")
	viper.AutomaticEnv()

	var err error
	if viper.GetBool("JSON_LOGS") {
		logger, err = zap.NewProduction()
	} else {
		logger, err = zap.NewDevelopment()
	}
	if err != nil {
		panic(err)
	}

}

func GetLogger() *zap.Logger {
	return logger
}

func GetDiscordSession() (*discordgo.Session, error) {
	discordAuthString := fmt.Sprintf("Bot %s", viper.GetString("DISCORD_TOKEN"))

	if discordAuthString == "" {
		return nil, fmt.Errorf("DISCORD_TOKEN must be defined")
	}

	discordSession, err := discordgo.New(discordAuthString)
	if err != nil {
		return nil, err
	}

	return discordSession, nil
}

func GetOpenAISession() (*gpt.Client, error) {
	authToken := viper.GetString("OPENAI_AUTH_TOKEN")
	if authToken == "" {
		return nil, errors.New("No authToken is present in configuration")
	}
	client := gpt.NewClient(authToken)

	request := gpt.CompletionRequest{
		Model:     gpt.GPT3Ada,
		Prompt:    "are you alive?",
		Suffix:    "",
		MaxTokens: 5,
	}
	requestCtx, cancel := context.WithTimeout(context.Background(), time.Second*2)
	defer cancel()
	_, err := client.CreateCompletion(requestCtx, request)
	if err != nil {
		return nil, errors.Wrap(err, "OpenAPI client failed warmup request")
	}

	return client, nil
}
