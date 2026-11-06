package bot

import (
	"fmt"
	"strings"
)

// helpPath is where the help page lives under the image base URL (it is
// uploaded next to the gallery by gallery.PublishSite).
const helpPath = "/gallery/help.html"

// msgEditNeedsImage is sent when an edit is asked for but there is no image to
// edit. It is static, so no model call is made.
const msgEditNeedsImage = "Edit what? There's no picture here. Attach an image to your message, or reply to one of my drawings, and say `edit: <what to change>`. Nobody ever gives me anything to work with. Say `@Danbot help` if you want the whole sad list."

// isHelpRequest reports whether the prompt is just the word help: the whole
// message, so "help me write a poem" still goes to chat.
func isHelpRequest(prompt string) bool {
	p := strings.ToLower(strings.TrimSpace(prompt))
	p = strings.TrimSpace(strings.TrimRight(p, "!?."))
	return p == "help"
}

// helpText is the static `@Danbot help` reply, in the bot's voice. Keep it in
// step with the README usage section and gallery/site/help.html (see AGENTS.md).
func helpText(imageBaseURL string) string {
	return fmt.Sprintf(`Oh, you want help. Everybody does. Here is what I do, as far as I can remember:

**Chat:** just `+"`@Danbot <anything>`"+` and I will tell you a long story that goes nowhere.
**Thread:** add 🧵 to start a thread, where I remember what we said (you still have to @ me every time).
**Draw:** `+"`@Danbot 🎨 <idea>`"+` or `+"`@Danbot draw me a picture of <idea>`"+`. Stick figures, wax crayon, 1024x1024. The arms are supposed to be like that.
**Edit:** attach an image (or reply to one of my drawings) and say `+"`✏️ <change>`, `edit: <change>` or `redraw <change>`"+`. png, jpeg or webp, under 50MB. The result is 1024x1024.
**Summarise a link:** `+"`@Danbot <https link>`"+` on its own, `+"`📰 <link>` or `summarize <link>`"+`. One paragraph, from the first 12,000 characters of the page. https only.

More, with the sad details: %s%s`, strings.TrimRight(imageBaseURL, "/"), helpPath)
}
