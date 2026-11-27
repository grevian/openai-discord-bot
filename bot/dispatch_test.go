package bot

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"openai-discord-bot/bot/flags"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
)

const londonMsg = "Draw a picture of yourself taking a photo of London Ontario and its snow covered trees"

// decisionAnswer builds a Decisions result with one choice answer.
func decisionAnswer(t *testing.T, choice string, confidence float64) *openai.Decision {
	t.Helper()
	return decisionJSON(t, fmt.Sprintf(`{"type":"choice","name":"intent","choice":%q,"confidence":%v,"probabilities":[]}`, choice, confidence))
}

func decisionJSON(t *testing.T, answer string) *openai.Decision {
	t.Helper()
	var d openai.Decision
	if err := json.Unmarshal([]byte(`{"answers":[`+answer+`],"model":"gpt-6-luna","usage":{"input_tokens":10,"total_tokens":10}}`), &d); err != nil {
		t.Fatal(err)
	}
	return &d
}

// dispatchBot is a bot with intent_decisions on and the decision fake
// answering with d.
func dispatchBot(t *testing.T, d *openai.Decision) *testBot {
	t.Helper()
	tb := newTestBot(t)
	ff := newFakeFlags()
	ff.bools[IntentDecisions.Name()] = true
	tb.bot.flags = ff
	tb.decisions.fn = func(context.Context, openai.DecisionNewParams) (*openai.Decision, error) { return d, nil }
	tb.responses.fn = func(context.Context, responses.ResponseNewParams) (*responses.Response, error) {
		return textResponse("chatted"), nil
	}
	tb.images.generate = func(context.Context, openai.ImageGenerateParams) (*openai.ImagesResponse, error) {
		return imageResponse(base64.StdEncoding.EncodeToString(pngBytes)), nil
	}
	tb.images.edit = func(context.Context, openai.ImageEditParams) (*openai.ImagesResponse, error) {
		return imageResponse(base64.StdEncoding.EncodeToString(pngBytes)), nil
	}
	tb.imgStore.sources[editSrcURL] = pngBytes
	return tb
}

func TestDispatchDrawFromPlainText(t *testing.T) {
	tb := dispatchBot(t, nil)
	tb.decisions.fn = func(context.Context, openai.DecisionNewParams) (*openai.Decision, error) {
		return decisionAnswer(t, "draw", 0.93), nil
	}
	tb.bot.handleMessage(context.Background(), mention(londonMsg))
	if len(tb.images.generated) != 1 || tb.responses.callCount() != 0 {
		t.Fatalf("generated %d, chat calls %d", len(tb.images.generated), tb.responses.callCount())
	}
	if p := tb.images.generated[0].Prompt; !strings.Contains(p, "London Ontario") || strings.Contains(p, "<@") {
		t.Errorf("prompt %q", p)
	}
}

func TestDispatchEditFromPlainTextWithImage(t *testing.T) {
	tb := dispatchBot(t, decisionAnswer(t, "edit", 0.9))
	tb.bot.handleMessage(context.Background(), editMention("make the trees pink"))
	if len(tb.images.edited) != 1 || tb.responses.callCount() != 0 {
		t.Fatalf("edited %d, chat calls %d", len(tb.images.edited), tb.responses.callCount())
	}
}

func TestDispatchEditWithoutImageChats(t *testing.T) {
	tb := dispatchBot(t, decisionAnswer(t, "edit", 0.9))
	tb.bot.handleMessage(context.Background(), mention("make the trees pink"))
	if len(tb.images.edited) != 0 || tb.responses.callCount() != 1 {
		t.Fatalf("edited %d, chat calls %d", len(tb.images.edited), tb.responses.callCount())
	}
}

func TestDispatchSummarizeFromPlainText(t *testing.T) {
	tb := dispatchBot(t, decisionAnswer(t, "summarize", 0.9))
	links := &fakeLinks{log: tb.calls, text: "page"}
	tb.bot.links = links
	tb.bot.handleMessage(context.Background(), mention("what's this article about? https://example.com/a."))
	if len(links.urls) != 1 || links.urls[0] != "https://example.com/a" {
		t.Fatalf("extracted %v", links.urls)
	}
}

func TestDispatchHelpFeedbackIdea(t *testing.T) {
	tb := dispatchBot(t, decisionAnswer(t, "help", 0.9))
	tb.bot.handleMessage(context.Background(), mention("what can you even do?"))
	if sent := tb.discord.sentMessages(); len(sent) != 1 || sent[0].Content != helpText(testImageBaseURL) {
		t.Errorf("help: %+v", sent)
	}
	for _, kind := range []string{"feedback", "idea"} {
		tb, fs := withFeedback(t)
		ff := newFakeFlags()
		ff.bools[IntentDecisions.Name()] = true
		tb.bot.flags = ff
		tb.decisions.fn = func(context.Context, openai.DecisionNewParams) (*openai.Decision, error) {
			return decisionAnswer(t, kind, 0.95), nil
		}
		tb.bot.handleMessage(context.Background(), mention("the bot is slow"))
		items := fs.stored()
		if len(items) != 1 || items[0].Kind != kind || items[0].Text != "the bot is slow" {
			t.Errorf("%s: %+v", kind, items)
		}
	}
}

func TestDispatchFallsBackToChat(t *testing.T) {
	refusal := decisionJSON(t, `{"type":"refusal","name":"intent","refusal":"no"}`)
	cases := []struct {
		name string
		fn   func(context.Context, openai.DecisionNewParams) (*openai.Decision, error)
	}{
		{"low confidence", func(context.Context, openai.DecisionNewParams) (*openai.Decision, error) {
			return decisionAnswer(t, "draw", 0.5), nil
		}},
		{"chat", func(context.Context, openai.DecisionNewParams) (*openai.Decision, error) {
			return decisionAnswer(t, "chat", 0.99), nil
		}},
		{"unknown value", func(context.Context, openai.DecisionNewParams) (*openai.Decision, error) {
			return decisionAnswer(t, "dance", 0.99), nil
		}},
		{"refusal", func(context.Context, openai.DecisionNewParams) (*openai.Decision, error) { return refusal, nil }},
		{"no answers", func(context.Context, openai.DecisionNewParams) (*openai.Decision, error) {
			return &openai.Decision{}, nil
		}},
		{"429", func(context.Context, openai.DecisionNewParams) (*openai.Decision, error) {
			return nil, &openai.Error{StatusCode: http.StatusTooManyRequests}
		}},
		{"timeout", func(ctx context.Context, _ openai.DecisionNewParams) (*openai.Decision, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tb := dispatchBot(t, nil)
			tb.decisions.fn = tc.fn
			tb.bot.timeouts.OpenAIDecision = 20 * time.Millisecond
			tb.bot.handleMessage(context.Background(), mention(londonMsg))
			if tb.responses.callCount() != 1 || len(tb.images.generated) != 0 {
				t.Errorf("chat calls %d, generated %d", tb.responses.callCount(), len(tb.images.generated))
			}
		})
	}
}

func TestDispatchFatalErrorLatchesDegraded(t *testing.T) {
	tb := dispatchBot(t, nil)
	tb.decisions.fn = func(context.Context, openai.DecisionNewParams) (*openai.Decision, error) {
		return nil, &openai.Error{StatusCode: http.StatusUnauthorized}
	}
	tb.bot.handleMessage(context.Background(), mention("hello"))
	sent := tb.discord.sentMessages()
	if !tb.bot.degraded.Load() || tb.responses.callCount() != 0 ||
		len(sent) != 2 || sent[0].Channel != testOperatorChannel || sent[1].Content != msgDegraded {
		t.Errorf("degraded=%v chat=%d sent=%+v", tb.bot.degraded.Load(), tb.responses.callCount(), sent)
	}
}

func TestDispatchNotCalled(t *testing.T) {
	cases := []struct {
		name  string
		msg   string
		flag  bool
		degr  bool
		image bool
	}{
		{"flag off", "hello", false, false, false},
		{"degraded", "hello", true, true, false},
		{"draw emoji", "🎨 a cat", true, false, false},
		{"draw phrase", "draw me a picture of a cat", true, false, false},
		{"edit emoji", "✏️ a hat", true, false, true},
		{"bare link", "https://example.com/a", true, false, false},
		{"help", "help", true, false, false},
		{"feedback", "feedback it is slow", true, false, false},
		{"idea", "idea a hat", true, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tb := dispatchBot(t, decisionAnswer(t, "draw", 0.99))
			ff := newFakeFlags()
			ff.bools[IntentDecisions.Name()] = tc.flag
			tb.bot.flags = ff
			tb.bot.degraded.Store(tc.degr)
			tb.bot.links = &fakeLinks{log: tb.calls, text: "page"}
			tb.bot.feedback = &fakeFeedbackStore{}
			m := mention(tc.msg)
			if tc.image {
				m = editMention(tc.msg)
			}
			tb.bot.handleMessage(context.Background(), m)
			if n := tb.decisions.callCount(); n != 0 {
				t.Errorf("Decisions called %d times", n)
			}
			if tc.degr {
				if sent := tb.discord.sentMessages(); len(sent) != 1 || sent[0].Content != msgDegraded {
					t.Errorf("sent %+v", sent)
				}
			}
		})
	}
}

func TestDispatchRequestShape(t *testing.T) {
	tb := dispatchBot(t, decisionAnswer(t, "chat", 0.9))
	tb.bot.handleMessage(context.Background(), mention("hello"))
	tb.bot.handleMessage(context.Background(), editMention("what is https://example.com/a"))
	if len(tb.decisions.calls) != 2 {
		t.Fatalf("calls %d", len(tb.decisions.calls))
	}
	values := func(p openai.DecisionNewParams) string {
		raw, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	plain, rich := values(tb.decisions.calls[0]), values(tb.decisions.calls[1])
	if strings.Contains(plain, `"edit"`) || strings.Contains(plain, `"summarize"`) {
		t.Errorf("plain text offered edit or summarize: %s", plain)
	}
	if !strings.Contains(rich, `"value":"edit"`) || !strings.Contains(rich, `"value":"summarize"`) {
		t.Errorf("image+link request lacks edit or summarize: %s", rich)
	}
	p := tb.decisions.calls[0]
	if p.Model != "gpt-6-luna" || p.SafetyIdentifier.Value != userID {
		t.Errorf("model %q safety %q", p.Model, p.SafetyIdentifier.Value)
	}
	for _, want := range []string{"🎨", "✏️", "📰", "MUST"} {
		if !strings.Contains(rich, want) {
			t.Errorf("request lacks %q", want)
		}
	}
}

func TestIntentDecisionsRegisteredOffByDefault(t *testing.T) {
	def, ok := flagRegistry.Lookup(IntentDecisions.Name())
	if !ok || def.Kind != flags.KindBool || IntentDecisions.Default() {
		t.Fatalf("intent_decisions must be a registered bool flag, off by default: %+v", def)
	}
}
