package bot

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/openai/openai-go/v3/responses"
)

func decodeMsgs(t *testing.T, s string) []genAIMessage {
	t.Helper()
	var m []genAIMessage
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("not valid JSON %q: %v", s, err)
	}
	return m
}

func TestChatSpanCarriesMessageContent(t *testing.T) {
	rec := recordSpans(t)
	tb := newTestBot(t)
	tb.responses.fn = func(context.Context, responses.ResponseNewParams) (*responses.Response, error) {
		return textResponse("gloomy reply"), nil
	}
	tb.bot.handleMessage(context.Background(), mention("hello there"))

	a := spanAttrs(t, rec, "openai chat")
	in := decodeMsgs(t, a["gen_ai.input.messages"].AsString())
	last := in[len(in)-1]
	if last.Role != "user" || !strings.Contains(last.Parts[0].Content, "hello there") {
		t.Errorf("input messages: %+v", in)
	}
	out := decodeMsgs(t, a["gen_ai.output.messages"].AsString())
	if len(out) != 1 || out[0].Role != "assistant" || out[0].Parts[0].Content != "gloomy reply" || out[0].FinishReason != "stop" {
		t.Errorf("output messages: %+v", out)
	}
	var sys []genAIPart
	if err := json.Unmarshal([]byte(a["gen_ai.system_instructions"].AsString()), &sys); err != nil || len(sys) == 0 {
		t.Errorf("system instructions: %v %v", sys, err)
	}
}

func TestDrawSpanHasPromptNotImage(t *testing.T) {
	rec := recordSpans(t)
	tb := drawingBot(t, &fakeGallery{})
	tb.bot.handleMessage(context.Background(), mention("draw me a picture of a cat"))
	tb.bot.galleryWG.Wait()
	a := spanAttrs(t, rec, "openai draw")
	if !strings.Contains(a["gen_ai.input.messages"].AsString(), "cat") {
		t.Errorf("prompt missing: %v", a["gen_ai.input.messages"])
	}
	out := a["gen_ai.output.messages"].AsString()
	if !strings.Contains(out, "not recorded") || len(out) > 300 {
		t.Errorf("output must be a placeholder, got %q", out)
	}
}

func TestDispatchSpanHasContent(t *testing.T) {
	rec := recordSpans(t)
	tb := dispatchBot(t, decisionAnswer(t, "draw", 0.95))
	tb.bot.handleMessage(context.Background(), mention(londonMsg))
	tb.bot.galleryWG.Wait()
	a := spanAttrs(t, rec, "openai dispatch")
	in := decodeMsgs(t, a["gen_ai.input.messages"].AsString())
	if len(in) != 1 || in[0].Role != "user" || !strings.Contains(in[0].Parts[0].Content, "London") {
		t.Errorf("input: %+v", in)
	}
	if !strings.Contains(a["gen_ai.system_instructions"].AsString(), "Context:") {
		t.Errorf("system: %v", a["gen_ai.system_instructions"])
	}
	if !strings.Contains(a["gen_ai.output.messages"].AsString(), "draw") {
		t.Errorf("output: %v", a["gen_ai.output.messages"])
	}
}

func TestMessagesAttrTruncatesButStaysValid(t *testing.T) {
	big := strings.Repeat("é", 10000) // multi-byte, forces a rune-safe cut
	var msgs []genAIMessage
	for i := 0; i < 20; i++ {
		msgs = append(msgs, textMessage("user", big))
	}
	kv, ok := messagesAttr("k", msgs)
	if !ok {
		t.Fatal("expected an attribute")
	}
	s := kv.Value.AsString()
	if len(s) > maxContentAttrBytes {
		t.Errorf("attribute is %d bytes", len(s))
	}
	if !utf8.ValidString(s) {
		t.Error("invalid UTF-8")
	}
	got := decodeMsgs(t, s)
	if len(got) == 0 || !strings.HasSuffix(got[len(got)-1].Parts[0].Content, truncationMarker) {
		t.Errorf("last message not truncated with a marker")
	}
}

func TestParseInputMessagesOmitsImages(t *testing.T) {
	raw := `[{"role":"user","content":[{"type":"input_text","text":"look"},{"type":"input_image","image_url":"data:image/png;base64,AAAA"}]}]`
	msgs, _ := parseInputMessages([]byte(raw))
	b, _ := json.Marshal(msgs)
	if strings.Contains(string(b), "AAAA") || !strings.Contains(string(b), "look") {
		t.Errorf("got %s", b)
	}
}
