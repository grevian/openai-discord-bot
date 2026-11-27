package config

import (
	"os"
	"testing"
)

// newSettings returns the getter loadBotSettings takes, reading BOT_<name>
// from the process environment.
func newSettings() func(string) string {
	return func(name string) string { return os.Getenv("BOT_" + name) }
}

func TestLoadBotSettingsDefaults(t *testing.T) {
	t.Setenv("BOT_OPERATOR_CHANNEL_ID", "")
	t.Setenv("BOT_IMAGE_BASE_URL", "")

	got, err := loadBotSettings(newSettings())
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

	got, err := loadBotSettings(newSettings())
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
			if _, err := loadBotSettings(newSettings()); err == nil {
				t.Fatalf("expected an error for channel=%q base=%q", tc.channel, tc.baseURL)
			}
		})
	}
}

func TestLoadBotSettingsKagiKey(t *testing.T) {
	t.Setenv("BOT_OPERATOR_CHANNEL_ID", "")
	t.Setenv("BOT_IMAGE_BASE_URL", "")
	t.Setenv("BOT_KAGI_API_KEY", "")
	got, err := loadBotSettings(newSettings())
	if err != nil || got.KagiAPIKey != "" {
		t.Fatalf("unset key: (%q, %v), want empty and no error", got.KagiAPIKey, err)
	}

	t.Setenv("BOT_KAGI_API_KEY", " k-123 \n")
	got, err = loadBotSettings(newSettings())
	if err != nil || got.KagiAPIKey != "k-123" {
		t.Fatalf("set key: (%q, %v), want trimmed k-123", got.KagiAPIKey, err)
	}
}

func TestLoadBotSettingsOwnerUserID(t *testing.T) {
	t.Setenv("BOT_OPERATOR_CHANNEL_ID", "")
	t.Setenv("BOT_IMAGE_BASE_URL", "")
	cases := []struct{ in, want string }{
		{"", ""},
		{" 123456789012345678 ", "123456789012345678"},
		// Invalid values disable the flag command instead of stopping the bot.
		{"populated-out-of-band", ""},
		{"12345", ""},
		{"<@123456789012345678>", ""},
	}
	for _, tc := range cases {
		t.Setenv("BOT_OWNER_USER_ID", tc.in)
		got, err := loadBotSettings(newSettings())
		if err != nil || got.OwnerUserID != tc.want {
			t.Errorf("BOT_OWNER_USER_ID=%q: (%q, %v), want %q and no error", tc.in, got.OwnerUserID, err, tc.want)
		}
	}
}
