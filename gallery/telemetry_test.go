package gallery

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestPublishSpanRecordsCountAndBytes(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	defer otel.SetTracerProvider(prev)

	g := newTestGallery(defaultCfg, newMemStore())
	s := sub("g1", 1)
	s.Context = []string{"a", "b"}
	if err := g.Publish(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	spans := rec.Ended()
	if len(spans) != 1 || spans[0].Name() != "gallery.publish" {
		t.Fatalf("spans: %v", spans)
	}
	got := map[attribute.Key]attribute.Value{}
	for _, kv := range spans[0].Attributes() {
		got[kv.Key] = kv.Value
	}
	if !got["gallery.eligible"].AsBool() || got["gallery.kind"].AsString() != string(KindGenerate) ||
		got["gallery.context_directives"].AsInt64() != 2 ||
		got["gallery.sidecar_bytes"].AsInt64() <= 0 || got["gallery.manifest_bytes"].AsInt64() <= 0 ||
		got["gallery.manifest_items"].AsInt64() != 1 || got["gallery.manifest_attempts"].AsInt64() != 1 {
		t.Errorf("attributes: %v", got)
	}
}
