package config

import (
	"log/slog"
	"strings"
	"testing"
)

func lookupFrom(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func TestLoadDefaults(t *testing.T) {
	c, err := Load(lookupFrom(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.DebugLogs || c.OTLPLogs || !c.JSONLogs || !c.Tracing || !c.Metrics {
		t.Errorf("bool defaults wrong: %+v", c)
	}
	if c.AWSRegion != "ca-central-1" {
		t.Errorf("AWSRegion = %q, want the historical region", c.AWSRegion)
	}
	if c.PromptPath != "prompts/danbo.json" {
		t.Errorf("PromptPath = %q", c.PromptPath)
	}
}

func TestLoadFromEnv(t *testing.T) {
	c, err := Load(lookupFrom(map[string]string{
		"BOT_JSON_LOGS":                     "false",
		"BOT_TRACING":                       "0",
		"BOT_METRICS":                       "garbage", // unparsable is false, as with viper
		"BOT_DEBUG_LOGS":                    "true",
		"BOT_DISCORD_TOKEN":                 " tok \n",
		"BOT_OPENAI_AUTH_TOKEN":             "sk",
		"BOT_OPENAIDISCORDBOTIMAGES_NAME":   "bucket",
		"BOT_FLAGS_TABLE":                   " flags ",
		"AWS_REGION":                        "eu-west-1",
		"BOT_PROMPT_PATH":                   "/etc/p.json",
		"BOT_CONVERSATION_TABLE":            "old",
		"AI_DISCORD_BOT_CONVERSATIONS_NAME": "conv",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.JSONLogs || c.Tracing || c.Metrics || !c.DebugLogs {
		t.Errorf("bools wrong: %+v", c)
	}
	if c.DiscordToken != "tok" || c.OpenAIToken != "sk" || c.ImagesBucket != "bucket" || c.FlagsTable != "flags" {
		t.Errorf("strings wrong: %+v", c)
	}
	if c.AWSRegion != "eu-west-1" || c.PromptPath != "/etc/p.json" {
		t.Errorf("region/prompt wrong: %+v", c)
	}
	if c.ConversationTable != "conv" {
		t.Errorf("ConversationTable = %q, AI_DISCORD_BOT_CONVERSATIONS_NAME must win", c.ConversationTable)
	}
}

func TestLoadEmptyCountsAsUnset(t *testing.T) {
	c, err := Load(lookupFrom(map[string]string{"BOT_JSON_LOGS": "", "AWS_REGION": "", "AI_DISCORD_BOT_CONVERSATIONS_NAME": "", "BOT_CONVERSATION_TABLE": "t"}))
	if err != nil {
		t.Fatal(err)
	}
	if !c.JSONLogs || c.AWSRegion != "ca-central-1" || c.ConversationTable != "t" {
		t.Errorf("empty values should fall back: %+v", c)
	}
}

func TestLoadRejectsInvalidSettings(t *testing.T) {
	if _, err := Load(lookupFrom(map[string]string{"BOT_OPERATOR_CHANNEL_ID": "general"})); err == nil {
		t.Fatal("expected an error for a bad channel ID")
	}
}

func TestConfigLogValueRedactsSecrets(t *testing.T) {
	c := Config{DiscordToken: "d-secret", OpenAIToken: "o-secret"}
	c.Bot.KagiAPIKey = "k-secret"
	out := slog.Any("config", c).Value.Resolve().String()
	for _, s := range []string{"d-secret", "o-secret", "k-secret"} {
		if strings.Contains(out, s) {
			t.Errorf("log value leaks %q: %s", s, out)
		}
	}
	if !strings.Contains(out, "<REDACTED>") {
		t.Errorf("expected redaction markers: %s", out)
	}
}
