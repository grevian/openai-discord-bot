package bot

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"
)

// Conversation content on OpenAI call spans, in the OTel GenAI semantic
// convention format: gen_ai.input.messages and gen_ai.output.messages are JSON
// arrays of {"role", "parts":[{"type":"text","content"}], "finish_reason"},
// and gen_ai.system_instructions is a JSON array of parts. Message text is
// already logged to Honeycomb at Info, so this adds no new exposure. Image
// bytes (inputs and outputs) never go on a span; a placeholder says so.
//
// Honeycomb accepts string attributes up to 64KB. Content is truncated rather
// than dropped: each text part is capped, and when the whole attribute is still
// too large the oldest messages are left out.
const (
	maxContentPartBytes = 6000
	maxContentAttrBytes = 32000
	truncationMarker    = "…[truncated]"
)

type genAIPart struct {
	Type    string `json:"type"`
	Content string `json:"content"`
}

type genAIMessage struct {
	Role         string      `json:"role"`
	Parts        []genAIPart `json:"parts"`
	FinishReason string      `json:"finish_reason,omitempty"`
}

func textMessage(role, text string) genAIMessage {
	return genAIMessage{Role: role, Parts: []genAIPart{{Type: "text", Content: text}}}
}

// truncateText cuts s to at most max bytes (on a rune boundary), ending in a
// marker when it had to cut.
func truncateText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max - len(truncationMarker)
	if cut < 0 {
		cut = 0
	}
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + truncationMarker
}

// messagesAttr encodes msgs as the attribute value, capping each part and
// dropping the oldest messages until the JSON fits. It returns false when
// there is nothing to record.
func messagesAttr(key string, msgs []genAIMessage) (attribute.KeyValue, bool) {
	if len(msgs) == 0 {
		return attribute.KeyValue{}, false
	}
	capped := make([]genAIMessage, len(msgs))
	for i, m := range msgs {
		parts := make([]genAIPart, len(m.Parts))
		for j, p := range m.Parts {
			p.Content = truncateText(p.Content, maxContentPartBytes)
			parts[j] = p
		}
		m.Parts = parts
		capped[i] = m
	}
	for {
		b, err := json.Marshal(capped)
		if err != nil {
			return attribute.KeyValue{}, false
		}
		if len(b) <= maxContentAttrBytes || len(capped) == 1 {
			return attribute.String(key, string(b)), true
		}
		capped = capped[1:]
	}
}

// parseInputMessages turns a marshalled request input (a plain string, or an
// array of role/content items as the Responses and Decisions APIs take) into
// GenAI messages. System and developer messages come back separately as system
// instructions. Non-text content parts (images) become a placeholder.
func parseInputMessages(raw []byte) (msgs []genAIMessage, system []genAIPart) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		if text != "" {
			msgs = append(msgs, textMessage("user", text))
		}
		return
	}
	var items []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &items) != nil {
		return
	}
	for _, it := range items {
		if it.Role == "" {
			continue
		}
		var parts []genAIPart
		var s string
		if json.Unmarshal(it.Content, &s) == nil {
			parts = []genAIPart{{Type: "text", Content: s}}
		} else {
			var cps []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if json.Unmarshal(it.Content, &cps) != nil {
				continue
			}
			for _, cp := range cps {
				if cp.Text != "" {
					parts = append(parts, genAIPart{Type: "text", Content: cp.Text})
				} else {
					parts = append(parts, genAIPart{Type: "text", Content: "[" + cp.Type + " omitted]"})
				}
			}
		}
		if it.Role == "developer" || it.Role == "system" {
			system = append(system, parts...)
			continue
		}
		msgs = append(msgs, genAIMessage{Role: it.Role, Parts: parts})
	}
	return
}

// setInput records the request content on the span. A nil/empty value
// records nothing.
func (c *aiCall) setInput(msgs []genAIMessage, system []genAIPart) {
	if kv, ok := messagesAttr("gen_ai.input.messages", msgs); ok {
		c.span.SetAttributes(kv)
	}
	if len(system) > 0 {
		sys := make([]genAIPart, len(system))
		for i, p := range system {
			p.Content = truncateText(p.Content, maxContentPartBytes)
			sys[i] = p
		}
		if b, err := json.Marshal(sys); err == nil && len(b) <= maxContentAttrBytes {
			c.span.SetAttributes(attribute.String("gen_ai.system_instructions", string(b)))
		}
	}
}

// setInputJSON records an SDK request input (marshalled to JSON) plus the
// instructions string, if any.
func (c *aiCall) setInputJSON(input any, instructions string) {
	msgs, system := parseInputMessages(mustJSON(input))
	if strings.TrimSpace(instructions) != "" {
		system = append([]genAIPart{{Type: "text", Content: instructions}}, system...)
	}
	c.setInput(msgs, system)
}

// setOutput records the response content on the span.
func (c *aiCall) setOutput(msgs ...genAIMessage) {
	if kv, ok := messagesAttr("gen_ai.output.messages", msgs); ok {
		c.span.SetAttributes(kv)
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
