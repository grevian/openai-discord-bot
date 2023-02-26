package config

import (
	"context"
	"fmt"
	"net/http"
	"openai-discord-bot/bot/storage"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/bwmarrin/discordgo"
	gpt "github.com/sashabaranov/go-gpt3"
	"github.com/spf13/viper"
	"go.opentelemetry.io/contrib/instrumentation/github.com/aws/aws-sdk-go-v2/otelaws"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.uber.org/zap"
)

var logger *zap.Logger
var awscfg aws.Config

func Configure(serviceCtx context.Context) {
	viper.SetEnvPrefix("BOT")
	viper.AutomaticEnv()

	if conversationsTableName, ok := os.LookupEnv("AI_DISCORD_BOT_CONVERSATIONS_NAME"); ok {
		viper.Set("CONVERSATION_TABLE", conversationsTableName)
	}

	var err error
	if viper.GetBool("JSON_LOGS") {
		logger, err = zap.NewProduction()
	} else {
		logger, err = zap.NewDevelopment()
	}
	if err != nil {
		panic(err)
	}

	logger.Info("Configuring tracing")
	tracingErr := configureTracing(serviceCtx, logger)
	if tracingErr != nil {
		logger.Error("failed to initialize tracer", zap.Error(tracingErr))
	}

	logger.Info("Configuring AWS Session")
	// TODO Can I remove the region? And should I be using the serviceCtx here?
	awscfg, err = config.LoadDefaultConfig(context.Background(), config.WithRegion("ca-central-1"))
	if err != nil {
		panic(fmt.Sprintf("unable to load SDK config, %v", err))
	}
	otelaws.AppendMiddlewares(&awscfg.APIOptions)
}

func GetAWSConfig() aws.Config {
	return awscfg
}

func GetStorage() *storage.Storage {
	return storage.NewStorage(GetAWSConfig())
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
	discordSession.Client.Transport = otelhttp.NewTransport(http.DefaultTransport)
	return discordSession, nil
}

func GetOpenAISession() (*gpt.Client, error) {
	authToken := viper.GetString("OPENAI_AUTH_TOKEN")
	if authToken == "" {
		return nil, fmt.Errorf("no authToken is present in configuration")
	}
	client := gpt.NewClient(authToken)
	client.HTTPClient = &http.Client{
		Transport: otelhttp.NewTransport(http.DefaultTransport),
	}

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
		return nil, fmt.Errorf("openAPI client failed warmup request: %w", err)
	}

	return client, nil
}
