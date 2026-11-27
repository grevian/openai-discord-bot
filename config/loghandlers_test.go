package config

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestLevelHandlerDropsDebugUntilEnabled(t *testing.T) {
	var buf bytes.Buffer
	lvl := new(slog.LevelVar)
	l := slog.New(newLevelHandler(lvl, slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	l.Debug("hidden")
	l.Info("shown")
	if strings.Contains(buf.String(), "hidden") || !strings.Contains(buf.String(), "shown") {
		t.Fatalf("debug should be dropped, info kept: %q", buf.String())
	}

	lvl.Set(slog.LevelDebug)
	l.With("k", "v").WithGroup("g").Debug("now visible")
	if !strings.Contains(buf.String(), "now visible") {
		t.Fatalf("debug should pass once enabled: %q", buf.String())
	}
}

func TestFanoutHandlerWritesToAllAndFiltersPerHandler(t *testing.T) {
	var a, b bytes.Buffer
	ha := slog.NewJSONHandler(&a, &slog.HandlerOptions{Level: slog.LevelDebug})
	hb := newLevelHandler(slog.LevelWarn, slog.NewJSONHandler(&b, &slog.HandlerOptions{Level: slog.LevelDebug}))
	l := slog.New(newFanoutHandler(ha, hb)).With("k", "v")

	l.InfoContext(context.Background(), "info msg")
	l.Warn("warn msg")
	if !strings.Contains(a.String(), "info msg") || !strings.Contains(a.String(), "warn msg") {
		t.Fatalf("first handler missing records: %q", a.String())
	}
	if strings.Contains(b.String(), "info msg") || !strings.Contains(b.String(), "warn msg") || !strings.Contains(b.String(), `"k":"v"`) {
		t.Fatalf("second handler wrong: %q", b.String())
	}
}
