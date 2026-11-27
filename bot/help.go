package bot

import (
	"fmt"
	"strings"
)

// helpPath is where the help page lives under the image base URL (it is
// uploaded next to the gallery by gallery.PublishSite).
const helpPath = "/help/"

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
**Draw:** `+"`@Danbot 🎨 <idea>`"+` or `+"`@Danbot draw me a picture of <idea>`"+`. Stick figures, wax crayon, 1024x1024. The arms are supposed to be like that. Add 🫧 to a draw or edit and I will skip my style and use your words as written.
**Edit:** attach an image (or reply to one of my drawings) and say `+"`✏️ <change>`, `edit: <change>` or `redraw <change>`"+`. png, jpeg or webp, under 50MB. The result is 1024x1024. When the owner has edit memory switched on, I keep earlier requests in mind: everything in a thread, or the last 20 messages of a reply chain (yours and mine) when you keep replying to my drawings.
**Summarise a link:** `+"`@Danbot <https link>`"+` on its own, `+"`📰 <link>` or `summarize <link>`"+`. One paragraph of what I make of it, from the first 4,000 characters of the page. https only.

**Plain words:** when the owner has guessing switched on, you can skip the emoji: `+"`@Danbot draw a fox in the snow`"+`, or reply to my drawing with `+"`make it night`"+`. I only guess when I am fairly sure, otherwise I chat. The emoji always work.

**Feedback:** `+"`@Danbot feedback <text>` or `@Danbot idea <text>`"+` at the start of the message. I write it down for the owner, with your Discord name and ID, and the owner (and the owner's AI agents) read it. Up to 1,500 characters, 5 per 10 minutes.

More, with the sad details: %s%s`, strings.TrimRight(imageBaseURL, "/"), helpPath)
}
