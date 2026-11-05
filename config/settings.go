package config

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/viper"

	"openai-discord-bot/bot"
)

const (
	// defaultOperatorChannelID is the channel the bot announces its startup and
	// shutdown in. Override with BOT_OPERATOR_CHANNEL_ID.
	defaultOperatorChannelID = "1091532074495787049"
	// defaultImageBaseURL is the public origin archived images are served from
	// (CloudFront). Override with BOT_IMAGE_BASE_URL.
	defaultImageBaseURL = "https://sillybullshit.click"
)

// loadBotSettings reads the operator-specific settings from v and validates
// them. Unset or empty variables fall back to the defaults above, so a
// deployment that sets nothing behaves exactly as before.
func loadBotSettings(v *viper.Viper) (bot.Settings, error) {
	v.SetDefault("OPERATOR_CHANNEL_ID", defaultOperatorChannelID)
	v.SetDefault("IMAGE_BASE_URL", defaultImageBaseURL)

	channelID := strings.TrimSpace(v.GetString("OPERATOR_CHANNEL_ID"))
	if !isSnowflake(channelID) {
		return bot.Settings{}, fmt.Errorf("BOT_OPERATOR_CHANNEL_ID must be a Discord channel ID (17-20 digits), got %q", channelID)
	}

	imageBaseURL, err := parseImageBaseURL(v.GetString("IMAGE_BASE_URL"))
	if err != nil {
		return bot.Settings{}, err
	}

	return bot.Settings{
		OperatorChannelID: channelID,
		ImageBaseURL:      imageBaseURL,
	}, nil
}

// isSnowflake reports whether s looks like a Discord snowflake ID: 17 to 20
// decimal digits.
func isSnowflake(s string) bool {
	if len(s) < 17 || len(s) > 20 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// parseImageBaseURL requires an https URL with no query, fragment or
// credentials, and returns it without a trailing slash.
func parseImageBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("BOT_IMAGE_BASE_URL must be an https URL with no query, fragment or credentials, got %q", raw)
	}
	return strings.TrimRight(u.String(), "/"), nil
}
