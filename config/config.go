package config

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/bwmarrin/discordgo"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/spf13/viper"
	"go.opentelemetry.io/contrib/instrumentation/github.com/aws/aws-sdk-go-v2/otelaws"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace/noop"
	"openai-discord-bot/bot/storage"
)

var awscfg aws.Config

func init() {
	viper.SetDefault("DEBUG_LOGS", false)
	viper.SetDefault("JSON_LOGS", true)
	viper.SetDefault("TRACING", true)
	viper.SetDefault("OPENAIDISCORDBOTIMAGES_NAME", "")
	viper.SetEnvPrefix("BOT")
	viper.AutomaticEnv()

	if conversationsTableName, ok := os.LookupEnv("AI_DISCORD_BOT_CONVERSATIONS_NAME"); ok {
		viper.Set("CONVERSATION_TABLE", conversationsTableName)
	}
}

func Configure(serviceCtx context.Context) {
	var err error
	err = configureLogging(serviceCtx)
	if err != nil {
		panic(err)
	}

	logger := slog.Default()

	configValues := viper.AllSettings()
	configFields := make([]any, 0, len(configValues))
	for k, v := range configValues {
		if k == "DISCORD_TOKEN" || k == "OPENAI_AUTH_TOKEN" {
			v = "<REDACTED>"
		}
		configFields = append(configFields, slog.Any(k, v))
	}
	logger.DebugContext(serviceCtx, "Configuration Loaded", configFields...)

	if viper.GetBool("TRACING") {
		logger.InfoContext(serviceCtx, "Configuring tracing")
		tracingErr := configureTracing(serviceCtx)
		if tracingErr != nil {
			logger.ErrorContext(serviceCtx, "failed to initialize tracer", slog.Any("error", tracingErr))
		}
	} else {
		otel.SetTracerProvider(noop.NewTracerProvider())
	}

	logger.InfoContext(serviceCtx, "Configuring AWS Session")
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

func GetImageStorage() *storage.ImageStorage {
	return storage.NewImageStorage(GetAWSConfig(), viper.GetString("OPENAIDISCORDBOTIMAGES_NAME"))
}

func GetLogger() *slog.Logger {
	return slog.Default()
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

func GetOpenAISession() (openai.Client, error) {
	authToken := viper.GetString("OPENAI_AUTH_TOKEN")
	if authToken == "" {
		return openai.Client{}, fmt.Errorf("no authToken is present in configuration")
	}

	httpClient := &http.Client{
		Transport: otelhttp.NewTransport(http.DefaultTransport),
	}
	return openai.NewClient(
		option.WithAPIKey(authToken),
		option.WithHTTPClient(httpClient),
	), nil
}
