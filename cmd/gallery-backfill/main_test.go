package main

import (
	"testing"

	"openai-discord-bot/gallery"
)

func TestParsePrompt(t *testing.T) {
	r := parsePrompt("edit: make it blue (source: https://x/1/abc)")
	if r.prompt != "make it blue" || r.kind != gallery.KindEdit {
		t.Errorf("edit = %+v", r)
	}
	r = parsePrompt("a cat")
	if r.prompt != "a cat" || r.kind != gallery.KindGenerate {
		t.Errorf("gen = %+v", r)
	}
}

func TestParsePromptStripsLeadingMention(t *testing.T) {
	if r := parsePrompt("<@123> draw a cat"); r.prompt != "draw a cat" {
		t.Errorf("got %q", r.prompt)
	}
	if r := parsePrompt("<@!123> edit: x (source: u)"); r.kind != gallery.KindEdit || r.prompt != "x" {
		t.Errorf("got %+v", r)
	}
}
