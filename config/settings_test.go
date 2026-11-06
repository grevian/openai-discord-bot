package config

import (
	"testing"

	"github.com/spf13/viper"
)

// newSettingsViper mirrors the global viper setup in init(): BOT_ prefix,
// read from the environment.
func newSettingsViper() *viper.Viper {
	v := viper.New()
	v.SetEnvPrefix("BOT")
	v.AutomaticEnv()
	return v
}

func TestLoadBotSettingsDefaults(t *testing.T) {
	t.Setenv("BOT_OPERATOR_CHANNEL_ID", "")
	t.Setenv("BOT_IMAGE_BASE_URL", "")

	got, err := loadBotSettings(newSettingsViper())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.OperatorChannelID != "1091532074495787049" {
		t.Errorf("OperatorChannelID = %q, want the historical default", got.OperatorChannelID)
	}
	if got.ImageBaseURL != "https://sillybullshit.click" {
		t.Errorf("ImageBaseURL = %q, want the historical default", got.ImageBaseURL)
	}
}

func TestLoadBotSettingsFromEnv(t *testing.T) {
	t.Setenv("BOT_OPERATOR_CHANNEL_ID", " 123456789012345678 ")
	t.Setenv("BOT_IMAGE_BASE_URL", "https://images.example.org/archive/")

	got, err := loadBotSettings(newSettingsViper())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.OperatorChannelID != "123456789012345678" {
		t.Errorf("OperatorChannelID = %q", got.OperatorChannelID)
	}
	if got.ImageBaseURL != "https://images.example.org/archive" {
		t.Errorf("ImageBaseURL = %q, want the trailing slash trimmed", got.ImageBaseURL)
	}
}

func TestLoadBotSettingsRejectsInvalid(t *testing.T) {
	cases := []struct {
		name    string
		channel string
		baseURL string
	}{
		{"channel name", "general", "https://example.com"},
		{"channel too short", "1234567890123456", "https://example.com"},
		{"channel too long", "123456789012345678901", "https://example.com"},
		{"channel with sign", "-12345678901234567", "https://example.com"},
		{"channel with letters", "10915320744957870a", "https://example.com"},
		{"image url http", "1091532074495787049", "http://example.com"},
		{"image url no scheme", "1091532074495787049", "example.com"},
		{"image url no host", "1091532074495787049", "https:///path"},
		{"image url query", "1091532074495787049", "https://example.com/?x=1"},
		{"image url fragment", "1091532074495787049", "https://example.com/#x"},
		{"image url credentials", "1091532074495787049", "https://user:pw@example.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("BOT_OPERATOR_CHANNEL_ID", tc.channel)
			t.Setenv("BOT_IMAGE_BASE_URL", tc.baseURL)
			if _, err := loadBotSettings(newSettingsViper()); err == nil {
				t.Fatalf("expected an error for channel=%q base=%q", tc.channel, tc.baseURL)
			}
		})
	}
}

func TestLoadBotSettingsKagiKey(t *testing.T) {
	t.Setenv("BOT_OPERATOR_CHANNEL_ID", "")
	t.Setenv("BOT_IMAGE_BASE_URL", "")
	t.Setenv("BOT_KAGI_API_KEY", "")
	got, err := loadBotSettings(newSettingsViper())
	if err != nil || got.KagiAPIKey != "" {
		t.Fatalf("unset key: (%q, %v), want empty and no error", got.KagiAPIKey, err)
	}

	t.Setenv("BOT_KAGI_API_KEY", " k-123 \n")
	got, err = loadBotSettings(newSettingsViper())
	if err != nil || got.KagiAPIKey != "k-123" {
		t.Fatalf("set key: (%q, %v), want trimmed k-123", got.KagiAPIKey, err)
	}
}

func TestIsSecretSetting(t *testing.T) {
	for _, k := range []string{"discord_token", "OPENAI_AUTH_TOKEN", "kagi_api_key"} {
		if !isSecretSetting(k) {
			t.Errorf("%s should be redacted", k)
		}
	}
	if isSecretSetting("image_base_url") {
		t.Error("image_base_url is not a secret")
	}
}
