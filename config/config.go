package config

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/bwmarrin/discordgo"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace/noop"
	"openai-discord-bot/bot"
	"openai-discord-bot/bot/feedback"
	"openai-discord-bot/bot/flags"
	"openai-discord-bot/bot/storage"
	"openai-discord-bot/config/awstrace"
	"openai-discord-bot/gallery"
	"openai-discord-bot/telemetry"
)

// Default values for settings the environment may leave out.
const (
	defaultAWSRegion  = "ca-central-1"
	defaultPromptPath = "prompts/danbo.json"
	defaultHealthAddr = "127.0.0.1:8086"
)

// Config is every setting the bot reads from the environment, loaded once by
// Load and passed explicitly. Environment variable names are a contract with
// the deployment (user-data, README): don't rename them.
type Config struct {
	DebugLogs bool // BOT_DEBUG_LOGS, default false
	JSONLogs  bool // BOT_JSON_LOGS, default true
	OTLPLogs  bool // BOT_OTLP_LOGS, default false
	Tracing   bool // BOT_TRACING, default true
	Metrics   bool // BOT_METRICS, default true

	DiscordToken string // BOT_DISCORD_TOKEN (secret, required)
	OpenAIToken  string // BOT_OPENAI_AUTH_TOKEN (secret, required)

	ImagesBucket      string // BOT_OPENAIDISCORDBOTIMAGES_NAME, optional
	ConversationTable string // AI_DISCORD_BOT_CONVERSATIONS_NAME, or BOT_CONVERSATION_TABLE
	FlagsTable        string // BOT_FLAGS_TABLE, optional
	FeedbackTable     string // BOT_FEEDBACK_TABLE, optional

	AWSRegion  string // AWS_REGION, default ca-central-1
	PromptPath string // BOT_PROMPT_PATH, default prompts/danbo.json
	HealthAddr string // BOT_HEALTH_ADDR, default 127.0.0.1:8086; "off" disables (empty here)

	Bot bot.Settings
}

// Load reads the configuration through lookup (os.LookupEnv in production).
// As before, an empty variable counts as unset.
func Load(lookup func(string) (string, bool)) (Config, error) {
	get := func(name string) string {
		v, _ := lookup(name)
		return v
	}
	bot := func(name string) string { return get("BOT_" + name) }
	flag := func(name string, def bool) bool {
		raw := bot(name)
		if raw == "" {
			return def
		}
		// Like viper's GetBool: anything strconv can't parse is false.
		b, _ := strconv.ParseBool(raw)
		return b
	}
	orDefault := func(v, def string) string {
		if strings.TrimSpace(v) == "" {
			return def
		}
		return strings.TrimSpace(v)
	}

	settings, err := loadBotSettings(bot)
	if err != nil {
		return Config{}, err
	}
	healthAddr, err := parseHealthAddr(bot("HEALTH_ADDR"))
	if err != nil {
		return Config{}, err
	}
	table := bot("CONVERSATION_TABLE")
	if v := get("AI_DISCORD_BOT_CONVERSATIONS_NAME"); v != "" {
		table = v
	}
	return Config{
		DebugLogs:         flag("DEBUG_LOGS", false),
		JSONLogs:          flag("JSON_LOGS", true),
		OTLPLogs:          flag("OTLP_LOGS", false),
		Tracing:           flag("TRACING", true),
		Metrics:           flag("METRICS", true),
		DiscordToken:      strings.TrimSpace(bot("DISCORD_TOKEN")),
		OpenAIToken:       bot("OPENAI_AUTH_TOKEN"),
		ImagesBucket:      bot("OPENAIDISCORDBOTIMAGES_NAME"),
		ConversationTable: table,
		FlagsTable:        strings.TrimSpace(bot("FLAGS_TABLE")),
		FeedbackTable:     strings.TrimSpace(bot("FEEDBACK_TABLE")),
		AWSRegion:         orDefault(get("AWS_REGION"), defaultAWSRegion),
		PromptPath:        orDefault(bot("PROMPT_PATH"), defaultPromptPath),
		HealthAddr:        healthAddr,
		Bot:               settings,
	}, nil
}

// parseHealthAddr validates BOT_HEALTH_ADDR: unset gives the default, "off"
// gives "" (disabled), anything else must be host:port with a port of 1-65535.
func parseHealthAddr(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultHealthAddr, nil
	}
	if strings.EqualFold(raw, "off") {
		return "", nil
	}
	_, port, err := net.SplitHostPort(raw)
	if err != nil {
		return "", fmt.Errorf("BOT_HEALTH_ADDR %q: %w", raw, err)
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return "", fmt.Errorf("BOT_HEALTH_ADDR %q: port must be 1-65535", raw)
	}
	return raw, nil
}

// LogValue lists the settings for the debug log, with secrets redacted.
func (c Config) LogValue() slog.Value {
	redact := func(s string) string {
		if s == "" {
			return ""
		}
		return "<REDACTED>"
	}
	return slog.GroupValue(
		slog.Bool("debug_logs", c.DebugLogs), slog.Bool("json_logs", c.JSONLogs),
		slog.Bool("otlp_logs", c.OTLPLogs), slog.Bool("tracing", c.Tracing),
		slog.Bool("metrics", c.Metrics),
		slog.String("discord_token", redact(c.DiscordToken)),
		slog.String("openai_auth_token", redact(c.OpenAIToken)),
		slog.String("kagi_api_key", redact(c.Bot.KagiAPIKey)),
		slog.String("images_bucket", c.ImagesBucket),
		slog.String("conversation_table", c.ConversationTable),
		slog.String("flags_table", c.FlagsTable),
		slog.String("feedback_table", c.FeedbackTable),
		slog.String("aws_region", c.AWSRegion),
		slog.String("prompt_path", c.PromptPath),
		slog.String("health_addr", c.HealthAddr),
		slog.String("operator_channel_id", c.Bot.OperatorChannelID),
		slog.String("image_base_url", c.Bot.ImageBaseURL),
		slog.String("owner_user_id", c.Bot.OwnerUserID),
	)
}

// Configure sets up logging, tracing, metrics and the AWS SDK config, which
// it returns. It must run before anything else logs.
func Configure(serviceCtx context.Context, cfg Config) (aws.Config, error) {
	if err := configureLogging(serviceCtx, cfg); err != nil {
		return aws.Config{}, err
	}
	logger := slog.Default()
	logger.DebugContext(serviceCtx, "Configuration Loaded", slog.Any("config", cfg))

	if cfg.Tracing {
		logger.InfoContext(serviceCtx, "Configuring tracing")
		if err := configureTracing(serviceCtx); err != nil {
			logger.ErrorContext(serviceCtx, "failed to initialize tracer", slog.Any("error", err))
		}
	} else {
		otel.SetTracerProvider(noop.NewTracerProvider())
	}

	if cfg.Metrics {
		logger.InfoContext(serviceCtx, "Configuring metrics")
		if err := configureMetrics(serviceCtx); err != nil {
			logger.ErrorContext(serviceCtx, "failed to initialize metrics", slog.Any("error", err))
		}
	}

	logger.InfoContext(serviceCtx, "Configuring AWS Session")
	awscfg, err := config.LoadDefaultConfig(serviceCtx, config.WithRegion(cfg.AWSRegion))
	if err != nil {
		return aws.Config{}, fmt.Errorf("unable to load SDK config: %w", err)
	}
	awstrace.AppendMiddlewares(&awscfg.APIOptions)
	return awscfg, nil
}

func GetStorage(awscfg aws.Config, cfg Config) (*storage.Storage, error) {
	return storage.NewStorage(awscfg, cfg.ConversationTable)
}

// GetFlagsBackend returns the feature flag store, or nil (defaults only)
// when BOT_FLAGS_TABLE is not set.
func GetFlagsBackend(awscfg aws.Config, cfg Config) flags.Backend {
	if cfg.FlagsTable == "" {
		slog.Warn("BOT_FLAGS_TABLE is not set, feature flags stay at their defaults")
		return nil
	}
	return flags.NewDynamoBackend(awscfg, cfg.FlagsTable)
}

// GetFeedbackStore returns the feedback store, or nil when
// BOT_FEEDBACK_TABLE is not set (the bot then says feedback is not switched on).
func GetFeedbackStore(awscfg aws.Config, cfg Config) *feedback.Store {
	if cfg.FeedbackTable == "" {
		slog.Warn("BOT_FEEDBACK_TABLE is not set, feedback is disabled")
		return nil
	}
	return feedback.NewStore(awscfg, cfg.FeedbackTable)
}

func GetImageStorage(awscfg aws.Config, cfg Config) *storage.ImageStorage {
	return storage.NewImageStorage(awscfg, cfg.ImagesBucket)
}

// GetGallery returns the public gallery publisher, or nil when the images
// bucket is not configured or the gallery's policy cannot be loaded. The
// gallery is optional, so problems here are logged, never fatal.
func GetGallery(awscfg aws.Config, cfg Config) *gallery.Gallery {
	bucket := cfg.ImagesBucket
	if bucket == "" {
		slog.Warn("no images bucket configured, the gallery is disabled")
		return nil
	}
	gcfg, err := gallery.LoadConfig()
	if err != nil {
		slog.Error("failed to load the gallery config, the gallery is disabled", slog.Any("error", err))
		return nil
	}
	return gallery.New(gcfg, gallery.NewS3Store(awscfg, bucket))
}

func GetLogger() *slog.Logger {
	return slog.Default()
}

func GetDiscordSession(cfg Config) (*discordgo.Session, error) {
	token := cfg.DiscordToken
	if token == "" {
		return nil, fmt.Errorf("DISCORD_TOKEN must be defined")
	}

	discordSession, err := discordgo.New("Bot " + token)
	if err != nil {
		return nil, err
	}
	discordSession.Client.Transport = telemetry.DiscordTransport(otelhttp.NewTransport(http.DefaultTransport))
	return discordSession, nil
}

func GetOpenAISession(cfg Config) (openai.Client, error) {
	authToken := cfg.OpenAIToken
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
