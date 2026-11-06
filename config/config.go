package config

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/bwmarrin/discordgo"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/spf13/viper"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace/noop"
	"openai-discord-bot/bot"
	"openai-discord-bot/bot/storage"
	"openai-discord-bot/config/awstrace"
	"openai-discord-bot/gallery"
)

var (
	awscfg      aws.Config
	botSettings bot.Settings
)

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
		if isSecretSetting(k) {
			v = "<REDACTED>"
		}
		configFields = append(configFields, slog.Any(k, v))
	}
	logger.DebugContext(serviceCtx, "Configuration Loaded", configFields...)

	botSettings, err = loadBotSettings(viper.GetViper())
	if err != nil {
		panic(err)
	}

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
	awstrace.AppendMiddlewares(&awscfg.APIOptions)
}

// isSecretSetting reports whether the viper key k holds a secret that must
// not be logged. viper lowercases keys, so compare without case.
func isSecretSetting(k string) bool {
	switch strings.ToUpper(k) {
	case "DISCORD_TOKEN", "OPENAI_AUTH_TOKEN", "KAGI_API_KEY":
		return true
	}
	return false
}

func GetAWSConfig() aws.Config {
	return awscfg
}

// GetBotSettings returns the operator settings validated by Configure.
func GetBotSettings() bot.Settings {
	return botSettings
}

func GetStorage() *storage.Storage {
	return storage.NewStorage(GetAWSConfig())
}

func GetImageStorage() *storage.ImageStorage {
	return storage.NewImageStorage(GetAWSConfig(), viper.GetString("OPENAIDISCORDBOTIMAGES_NAME"))
}

// GetGallery returns the public gallery publisher, or nil when the images
// bucket is not configured or the gallery's policy cannot be loaded. The
// gallery is optional, so problems here are logged, never fatal.
func GetGallery() *gallery.Gallery {
	bucket := viper.GetString("OPENAIDISCORDBOTIMAGES_NAME")
	if bucket == "" {
		slog.Warn("no images bucket configured, the gallery is disabled")
		return nil
	}
	cfg, err := gallery.LoadConfig()
	if err != nil {
		slog.Error("failed to load the gallery config, the gallery is disabled", slog.Any("error", err))
		return nil
	}
	return gallery.New(cfg, gallery.NewS3Store(GetAWSConfig(), bucket))
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
