package telemetry

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestDiscordRoute(t *testing.T) {
	cases := map[string]string{
		"/api/v9/channels/1234567890/messages/9876543210":         "/channels/{id}/messages/{id}",
		"/api/v9/channels/1234567890/typing":                      "/channels/{id}/typing",
		"/api/v9/channels/1234567890/messages/9876543210/threads": "/channels/{id}/messages/{id}/threads",
	}
	for in, want := range cases {
		if got := DiscordRoute(in); got != want {
			t.Errorf("DiscordRoute(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDiscordTransportRecords429(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	defer otel.SetTracerProvider(prev)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "1.5")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	c := &http.Client{Transport: DiscordTransport(http.DefaultTransport)}
	resp, err := c.Get(srv.URL + "/api/v9/channels/1234567890/messages/9876543210")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	spans := rec.Ended()
	if len(spans) != 1 {
		t.Fatalf("got %d spans", len(spans))
	}
	got := map[attribute.Key]attribute.Value{}
	for _, kv := range spans[0].Attributes() {
		got[kv.Key] = kv.Value
	}
	if got["discord.route"].AsString() != "/channels/{id}/messages/{id}" ||
		!got["discord.rate_limited"].AsBool() ||
		got["discord.retry_after_s"].AsFloat64() != 1.5 ||
		got["http.response.status_code"].AsInt64() != 429 {
		t.Errorf("attributes: %v", got)
	}
}
