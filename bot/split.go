package bot

import "strings"

const (
	// discordMessageLimit is Discord's maximum message content length, in characters.
	discordMessageLimit = 2000
	// maxReplyMessages caps how many messages one chat reply may span.
	maxReplyMessages = 3
	// lengthHint is appended to the chat instructions so the model usually stays
	// within one message.
	lengthHint  = "Keep replies under 1,800 characters if you can; Discord cuts off anything much longer."
	truncMarker = "…"
)

// splitReply breaks text into at most maxMessages chunks of at most limit
// characters (runes), preferring paragraph, then line, then word boundaries.
// Anything that does not fit is dropped and the last chunk ends with "…".
func splitReply(text string, limit, maxMessages int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	var chunks []string
	rest := text
	for rest != "" {
		if len(chunks) == maxMessages {
			// Truncated: mark the last chunk, making room for the marker.
			last := []rune(chunks[len(chunks)-1])
			if len(last)+len([]rune(truncMarker)) > limit {
				last = last[:limit-len([]rune(truncMarker))]
			}
			chunks[len(chunks)-1] = strings.TrimRight(string(last), " \n") + truncMarker
			return chunks
		}
		head, tail := cutAt(rest, limit)
		chunks = append(chunks, head)
		rest = strings.TrimLeft(tail, " \n")
	}
	return chunks
}

// cutAt returns a prefix of s of at most limit runes, cut at the last paragraph
// break, else line break, else space within the limit (else a hard cut), and the
// remainder.
func cutAt(s string, limit int) (head, tail string) {
	r := []rune(s)
	if len(r) <= limit {
		return s, ""
	}
	window := string(r[:limit+1]) // one extra rune so a break right at the limit counts
	for _, sep := range []string{"\n\n", "\n", " "} {
		if i := strings.LastIndex(window, sep); i > 0 {
			h := strings.TrimRight(window[:i], " \n")
			if h != "" && len([]rune(h)) <= limit {
				return h, s[i:]
			}
		}
	}
	return string(r[:limit]), string(r[limit:])
}
