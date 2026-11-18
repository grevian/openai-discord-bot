package bot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// recordSpans installs an in-memory tracer provider for the test.
func recordSpans(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })
	return rec
}

// spanAttrs returns the attributes of the one ended span called name.
func spanAttrs(t *testing.T, rec *tracetest.SpanRecorder, name string) map[attribute.Key]attribute.Value {
	t.Helper()
	var found map[attribute.Key]attribute.Value
	for _, s := range rec.Ended() {
		if s.Name() != name {
			continue
		}
		if found != nil {
			t.Fatalf("more than one %q span", name)
		}
		found = map[attribute.Key]attribute.Value{}
		for _, kv := range s.Attributes() {
			found[kv.Key] = kv.Value
		}
	}
	if found == nil {
		t.Fatalf("no %q span; have %v", name, spanNames(rec))
	}
	return found
}

func spanNames(rec *tracetest.SpanRecorder) []string {
	var n []string
	for _, s := range rec.Ended() {
		n = append(n, s.Name())
	}
	return n
}

func TestEstimateCostUSD(t *testing.T) {
	got, ok := estimateCostUSD(string(chatModel), 1_000_000, 2_000_000)
	want := modelPrices[string(chatModel)].inputPerM + 2*modelPrices[string(chatModel)].outputPerM
	if !ok || got != want {
		t.Errorf("cost = %v, %v; want %v", got, ok, want)
	}
	if _, ok := estimateCostUSD("unknown-model", 1, 1); ok {
		t.Error("an unpriced model must give no estimate")
	}
}

func TestChatSpanCarriesModelAndUsage(t *testing.T) {
	rec := recordSpans(t)
	tb := newTestBot(t)
	tb.responses.fn = func(context.Context, responses.ResponseNewParams) (*responses.Response, error) {
		r := textResponse("hi")
		r.ID = "resp_1"
		r.Usage = responses.ResponseUsage{InputTokens: 100, OutputTokens: 20, TotalTokens: 120}
		return r, nil
	}
	tb.bot.handleMessage(context.Background(), mention("hello"))

	a := spanAttrs(t, rec, "openai chat")
	if a["openai.operation"].AsString() != "chat" ||
		a["gen_ai.request.model"].AsString() != string(chatModel) ||
		a["gen_ai.usage.input_tokens"].AsInt64() != 100 ||
		a["gen_ai.usage.output_tokens"].AsInt64() != 20 ||
		a["gen_ai.response.id"].AsString() != "resp_1" {
		t.Errorf("attributes: %v", a)
	}
	if _, ok := a["openai.estimated_cost_usd"]; !ok {
		t.Error("missing estimated cost")
	}
	m := spanAttrs(t, rec, "messageCreate")
	if m["intent"].AsString() != "chat" || m["clean"].AsBool() || m["thread.messages"].AsInt64() != 0 {
		t.Errorf("messageCreate attributes: %v", m)
	}
}

func TestDrawSpansCarryIntentAndClean(t *testing.T) {
	rec := recordSpans(t)
	tb := drawingBot(t, &fakeGallery{})
	tb.bot.handleMessage(context.Background(), mention("draw me a picture of a cat 🫧"))
	tb.bot.galleryWG.Wait()

	for _, name := range []string{"messageCreate", "handleImageMessage"} {
		a := spanAttrs(t, rec, name)
		if a["intent"].AsString() != "draw" || !a["clean"].AsBool() {
			t.Errorf("%s attributes: %v", name, a)
		}
	}
	if a := spanAttrs(t, rec, "openai draw"); a["gen_ai.operation.name"].AsString() != "image_generation" {
		t.Errorf("openai draw attributes: %v", a)
	}
}

func TestReplyChainAndCondenseSpans(t *testing.T) {
	rec := recordSpans(t)
	tb, _ := replyChainBot(t, true)
	tb.responses.fn = func(context.Context, responses.ResponseNewParams) (*responses.Response, error) {
		return textResponse("- a disco party\n- hats"), nil
	}
	last := chainMessages(tb, "draw me a picture of a disco party", "edit: hats")
	tb.bot.handleMessage(context.Background(), replyTo(last, "edit: add another dancer"))
	tb.bot.galleryWG.Wait()

	c := spanAttrs(t, rec, "replyChain")
	if c["reply_chain.hops"].AsInt64() < 2 || c["reply_chain.messages_fetched"].AsInt64() < 1 || c["reply_chain.user_requests"].AsInt64() != 2 {
		t.Errorf("replyChain attributes: %v", c)
	}
	d := spanAttrs(t, rec, "condenseEditContext")
	if d["condense.directives"].AsInt64() != 2 || d["condense.source_messages"].AsInt64() != 2 {
		t.Errorf("condense attributes: %v", d)
	}
	if a := spanAttrs(t, rec, "openai condense"); a["openai.operation"].AsString() != "condense" {
		t.Errorf("openai condense attributes: %v", a)
	}
}

// The SDK retries 5xx itself; the middleware must see every attempt and the
// request id of the last one.
func TestAttemptTrackerCountsRetriesAndRequestID(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-Id", "req_abc")
		w.Header().Set("Content-Type", "application/json")
		if n.Add(1) == 1 {
			w.Header().Set("Retry-After-Ms", "1")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"boom"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"resp_2","usage":{"input_tokens":5,"output_tokens":6,"total_tokens":11}}`))
	}))
	defer srv.Close()
	client := openai.NewClient(option.WithAPIKey("k"), option.WithBaseURL(srv.URL), option.WithMaxRetries(2))

	rec := recordSpans(t)
	tb := newTestBot(t)
	tb.bot.responses = &client.Responses
	if _, err := tb.bot.responsesNew(context.Background(), opSummarize, responses.ResponseNewParams{Model: chatModel}); err != nil {
		t.Fatal(err)
	}
	a := spanAttrs(t, rec, "openai summarize")
	if a["openai.retries"].AsInt64() != 1 || a["openai.attempts"].AsInt64() != 2 ||
		a["openai.request_id"].AsString() != "req_abc" || a["gen_ai.usage.output_tokens"].AsInt64() != 6 {
		t.Errorf("attributes: %v", a)
	}
}
