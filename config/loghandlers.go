package config

import (
	"context"
	"errors"
	"log/slog"
)

// levelHandler drops records below level before they reach the wrapped
// handler. The OTLP SDK's batch processor accepts every level, so without this
// wrapper BOT_DEBUG_LOGS would have no effect on what is exported.
type levelHandler struct {
	level slog.Leveler
	next  slog.Handler
}

func newLevelHandler(level slog.Leveler, next slog.Handler) slog.Handler {
	return &levelHandler{level: level, next: next}
}

func (h *levelHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return l >= h.level.Level() && h.next.Enabled(ctx, l)
}

func (h *levelHandler) Handle(ctx context.Context, r slog.Record) error {
	return h.next.Handle(ctx, r)
}

func (h *levelHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &levelHandler{level: h.level, next: h.next.WithAttrs(attrs)}
}

func (h *levelHandler) WithGroup(name string) slog.Handler {
	return &levelHandler{level: h.level, next: h.next.WithGroup(name)}
}

// fanoutHandler sends each record to every handler that is enabled for it.
type fanoutHandler struct {
	handlers []slog.Handler
}

func newFanoutHandler(handlers ...slog.Handler) slog.Handler {
	return &fanoutHandler{handlers: handlers}
}

func (h *fanoutHandler) Enabled(ctx context.Context, l slog.Level) bool {
	for _, c := range h.handlers {
		if c.Enabled(ctx, l) {
			return true
		}
	}
	return false
}

func (h *fanoutHandler) Handle(ctx context.Context, r slog.Record) error {
	var errs []error
	for _, c := range h.handlers {
		if c.Enabled(ctx, r.Level) {
			if err := c.Handle(ctx, r.Clone()); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func (h *fanoutHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := make([]slog.Handler, len(h.handlers))
	for i, c := range h.handlers {
		next[i] = c.WithAttrs(attrs)
	}
	return &fanoutHandler{handlers: next}
}

func (h *fanoutHandler) WithGroup(name string) slog.Handler {
	next := make([]slog.Handler, len(h.handlers))
	for i, c := range h.handlers {
		next[i] = c.WithGroup(name)
	}
	return &fanoutHandler{handlers: next}
}
