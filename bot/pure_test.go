package bot

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/openai/openai-go/v3"
)

func TestDetectImageType(t *testing.T) {
	webp := append([]byte("RIFF\x00\x00\x00\x00WEBPVP"), make([]byte, 16)...)
	tests := []struct {
		name         string
		in           []byte
		wantType     string
		wantFilename string
	}{
		{"png", pngBytes, "image/png", "source.png"},
		{"jpeg", []byte("\xFF\xD8\xFF\xE0rest-of-jpeg"), "image/jpeg", "source.jpg"},
		{"webp", webp, "image/webp", "source.webp"},
		{"gif is unsupported", []byte("GIF89a......"), "", ""},
		{"text", []byte("hello world"), "", ""},
		{"empty", nil, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotType, gotName := detectImageType(tt.in)
			if gotType != tt.wantType || gotName != tt.wantFilename {
				t.Errorf("detectImageType = (%q, %q), want (%q, %q)", gotType, gotName, tt.wantType, tt.wantFilename)
			}
		})
	}
}

func TestIsImageAttachment(t *testing.T) {
	tests := []struct {
		name string
		att  *discordgo.MessageAttachment
		want bool
	}{
		{"nil", nil, false},
		{"content type", &discordgo.MessageAttachment{ContentType: "image/gif", Filename: "x"}, true},
		{"png name", &discordgo.MessageAttachment{Filename: "A.PNG"}, true},
		{"jpg name", &discordgo.MessageAttachment{Filename: "a.jpg"}, true},
		{"jpeg name", &discordgo.MessageAttachment{Filename: "a.jpeg"}, true},
		{"webp name", &discordgo.MessageAttachment{Filename: "a.webp"}, true},
		{"text", &discordgo.MessageAttachment{ContentType: "text/plain", Filename: "a.txt"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isImageAttachment(tt.att); got != tt.want {
				t.Errorf("isImageAttachment = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStripEditTriggers(t *testing.T) {
	tests := map[string]string{
		"✏️ add a hat":          "add a hat",
		"edit: make it blue":    "make it blue",
		"Edit: make it blue":    "make it blue",
		"EDIT: make it blue":    "make it blue",
		"redraw it as a cat":    "it as a cat",
		"Redraw it as a cat":    "it as a cat",
		"  nothing to strip   ": "nothing to strip",
	}
	for in, want := range tests {
		if got := stripEditTriggers(in); got != want {
			t.Errorf("stripEditTriggers(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUserWasMentioned(t *testing.T) {
	me := &discordgo.User{ID: botID}
	if userWasMentioned(nil, []*discordgo.User{me}) {
		t.Error("nil user must not count as mentioned")
	}
	if userWasMentioned(me, nil) {
		t.Error("no mentions must not count as mentioned")
	}
	if userWasMentioned(me, []*discordgo.User{{ID: "someone-else"}}) {
		t.Error("other users' mentions must not count")
	}
	if !userWasMentioned(me, []*discordgo.User{{ID: "someone-else"}, {ID: botID}}) {
		t.Error("expected the bot to be found in the mentions")
	}
}

func TestIsFatalOpenAIError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain error", errors.New("boom"), false},
		{"401", &openai.Error{StatusCode: http.StatusUnauthorized}, true},
		{"quota", &openai.Error{StatusCode: http.StatusTooManyRequests, Code: "insufficient_quota"}, true},
		{"invalid key", &openai.Error{StatusCode: http.StatusBadRequest, Code: "invalid_api_key"}, true},
		{"rate limit", &openai.Error{StatusCode: http.StatusTooManyRequests, Code: "rate_limit_exceeded"}, false},
		{"500", &openai.Error{StatusCode: http.StatusInternalServerError}, false},
		{"wrapped 401", fmt.Errorf("wrapped: %w", &openai.Error{StatusCode: http.StatusUnauthorized}), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isFatalOpenAIError(tt.err); got != tt.want {
				t.Errorf("isFatalOpenAIError = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestClassifyMessage(t *testing.T) {
	me := "<@" + botID + ">"
	tests := []struct {
		name       string
		content    string
		hasSource  bool
		wantIntent messageIntent
		wantPrompt string
	}{
		{"chat strips mention", me + " hello there", false, intentChat, " hello there"},
		{"draw phrase", me + " Draw me a picture of a Cat", false, intentDraw, "a Cat"},
		{"draw emoji", me + " 🎨 a Dog", false, intentDraw, "a Dog"},
		{"edit pencil", me + " ✏️ add a hat", true, intentEdit, "add a hat"},
		{"edit colon", me + " Edit: add a hat", true, intentEdit, "add a hat"},
		{"redraw", me + " redraw as a cat", true, intentEdit, "as a cat"},
		{"edit without source is chat", me + " edit: add a hat", false, intentChat, " edit: add a hat"},
		{"edit wins over draw", me + " 🎨 edit: add a hat", true, intentEdit, "🎨  add a hat"},
		{"source without trigger is chat", me + " nice picture", true, intentChat, " nice picture"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			intent, prompt := classifyMessage(tt.content, botID, tt.hasSource)
			if intent != tt.wantIntent || prompt != tt.wantPrompt {
				t.Errorf("classifyMessage = (%v, %q), want (%v, %q)", intent, prompt, tt.wantIntent, tt.wantPrompt)
			}
		})
	}
}

func TestFailureMessage(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"plain", errors.New("kaboom secret"), msgGeneric},
		{"rate limit", &openai.Error{StatusCode: http.StatusTooManyRequests, Code: "rate_limit_exceeded"}, msgBusy},
		{"overloaded", &openai.Error{StatusCode: http.StatusServiceUnavailable}, msgBusy},
		{"quota is not busy", &openai.Error{StatusCode: http.StatusTooManyRequests, Code: "insufficient_quota"}, msgGeneric},
		{"content policy", &openai.Error{StatusCode: http.StatusBadRequest, Code: "content_policy_violation"}, msgInvalidRequest},
		{"moderation", &openai.Error{StatusCode: http.StatusBadRequest, Code: "moderation_blocked"}, msgInvalidRequest},
		{"wrapped invalid", fmt.Errorf("w: %w", &openai.Error{StatusCode: http.StatusBadRequest}), msgInvalidRequest},
		{"500", &openai.Error{StatusCode: http.StatusInternalServerError}, msgGeneric},
		{"401", &openai.Error{StatusCode: http.StatusUnauthorized}, msgGeneric},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := failureMessage(tt.err); got != tt.want {
				t.Errorf("failureMessage = %q, want %q", got, tt.want)
			}
		})
	}
}
