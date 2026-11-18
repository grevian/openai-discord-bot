// Package telemetry holds the bot's instrumentation helpers that do not
// depend on the bot itself: the Discord REST transport and build version.
package telemetry

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Version is the git sha (or tag) of the build. The Docker build sets it with
// -ldflags "-X openai-discord-bot/telemetry.Version=<sha>".
var Version = "dev"

var snowflake = regexp.MustCompile(`^[0-9]{5,}$`)

// DiscordRoute turns a Discord REST path into a low-cardinality template:
// /api/v9/channels/123/messages/456 becomes /channels/{id}/messages/{id}.
// Only numeric snowflakes are replaced, so resource names stay readable.
func DiscordRoute(path string) string {
	path = strings.TrimPrefix(path, "/api")
	segs := strings.Split(path, "/")
	out := segs[:0]
	for i, s := range segs {
		switch {
		case i == 1 && len(s) > 1 && s[0] == 'v' && isDigits(s[1:]):
			continue // API version
		case snowflake.MatchString(s):
			s = "{id}"
		}
		out = append(out, s)
	}
	return strings.Join(out, "/")
}

func isDigits(s string) bool {
	_, err := strconv.Atoi(s)
	return err == nil
}

// DiscordTransport wraps next so every Discord REST call is a span named
// "discord METHOD route" with the route template, status, and on a 429 the
// retry-after. discordgo retries 429s itself, so each 429 shows up as its own
// short span.
func DiscordTransport(next http.RoundTripper) http.RoundTripper {
	return discordTransport{next: next}
}

type discordTransport struct{ next http.RoundTripper }

func (t discordTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	route := DiscordRoute(req.URL.Path)
	ctx, span := otel.GetTracerProvider().Tracer("AIBot").Start(req.Context(), "discord "+req.Method+" "+route,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("discord.route", route),
			attribute.String("http.request.method", req.Method),
		))
	defer span.End()
	resp, err := t.next.RoundTrip(req.WithContext(ctx))
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return resp, err
	}
	span.SetAttributes(attribute.Int("http.response.status_code", resp.StatusCode))
	if resp.StatusCode == http.StatusTooManyRequests {
		span.SetAttributes(attribute.Bool("discord.rate_limited", true))
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			if secs, perr := strconv.ParseFloat(ra, 64); perr == nil {
				span.SetAttributes(attribute.Float64("discord.retry_after_s", secs))
			}
		}
		span.SetAttributes(attribute.String("discord.rate_limit_scope", resp.Header.Get("X-RateLimit-Scope")))
	}
	if resp.StatusCode >= 500 {
		span.SetStatus(codes.Error, resp.Status)
	}
	return resp, nil
}
