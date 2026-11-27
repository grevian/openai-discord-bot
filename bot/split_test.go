package bot

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/openai/openai-go/v3/responses"
)

func TestSplitReply(t *testing.T) {
	para := func(c string, n int) string { return strings.Repeat(c, n) }
	cases := []struct {
		name string
		in   string
		want int // chunks
	}{
		{"short", "hello", 1},
		{"empty", "  ", 0},
		{"paragraphs", para("a", 1500) + "\n\n" + para("b", 1500), 2},
		{"one long word run", para("x", 4500), 3},
		{"truncated", para("a", 1900) + "\n\n" + para("b", 1900) + "\n\n" + para("c", 1900) + "\n\n" + para("d", 1900), 3},
		{"multibyte", para("é", 2500), 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := splitReply(tc.in, 2000, 3)
			if len(got) != tc.want {
				t.Fatalf("got %d chunks, want %d", len(got), tc.want)
			}
			for _, c := range got {
				if n := utf8.RuneCountInString(c); n > 2000 || n == 0 {
					t.Errorf("chunk has %d runes", n)
				}
			}
		})
	}
	// Paragraph boundaries are preferred and nothing is lost when it fits.
	got := splitReply(para("a", 1500)+"\n\n"+para("b", 1500), 2000, 3)
	if got[0] != para("a", 1500) || got[1] != para("b", 1500) {
		t.Errorf("bad split %q", got)
	}
	// Truncation is marked.
	got = splitReply(para("a", 9000), 2000, 3)
	if !strings.HasSuffix(got[2], "…") {
		t.Errorf("last chunk not marked truncated")
	}
}

func TestChatReplyOver2000CharsIsSplit(t *testing.T) {
	tb := newTestBot(t)
	long := strings.Repeat("a", 1500) + "\n\n" + strings.Repeat("b", 1500)
	tb.responses.fn = func(context.Context, responses.ResponseNewParams) (*responses.Response, error) {
		return textResponse(long), nil
	}
	tb.bot.handleMessage(context.Background(), mention("tell me a story"))
	got := tb.discord.sentMessages()
	if len(got) != 2 {
		t.Fatalf("expected 2 messages, got %+v", got)
	}
	for _, m := range got {
		if utf8.RuneCountInString(m.Content) > 2000 {
			t.Errorf("message of %d chars sent", len(m.Content))
		}
		if strings.Contains(m.Content, "Gah") {
			t.Errorf("error notice sent: %q", m.Content)
		}
	}
}
