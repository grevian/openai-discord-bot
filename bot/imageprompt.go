package bot

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// buildDrawPrompt puts the user's scene first, followed by the labelled style
// block (style, mood, exclusions) from the persona file.
func buildDrawPrompt(scene, styleBlock string) string {
	p := "Scene: " + scene
	if styleBlock != "" {
		p += "\n" + styleBlock
	}
	return p
}

// buildEditPrompt states the requested change first and then a short keep
// list. It deliberately does not reuse the full drawing style block, so an
// edit stays narrow; the keep list only names what must be preserved.
func buildEditPrompt(change, keepList string) string {
	p := "Edit the input image. Change only: " + strings.TrimSpace(change)
	if r, _ := utf8.DecodeLastRuneInString(p); !strings.ContainsRune(".?!", r) {
		p += "."
	}
	if keepList != "" {
		p += "\n" + keepList
	}
	return p
}

// cleanEmoji asks for an image without the danbot prompt. It is the marker in
// the request, and the bot puts it in the reply to a clean image so that
// edits made as a reply to that image stay clean.
const cleanEmoji = "🫧"

func hasCleanEmoji(s string) bool { return strings.Contains(s, cleanEmoji) }

// cleanEmojiRun is the marker with the blanks around it, so removing it
// from mid-text leaves one space, not two.
var cleanEmojiRun = regexp.MustCompile(`[ \t]*` + cleanEmoji + `[ \t]*`)

func stripCleanEmoji(s string) string {
	return strings.TrimSpace(cleanEmojiRun.ReplaceAllString(s, " "))
}

// cleanReply adds the clean marker to the reply text of a clean image.
func cleanReply(content string, clean bool) string {
	if clean {
		return content + " " + cleanEmoji
	}
	return content
}

// buildCleanEditPromptWithContext is the user's change as written, followed
// by any thread directives. No danbot framing or keep list is added.
func buildCleanEditPromptWithContext(change string, directives []string) string {
	if len(directives) == 0 {
		return change
	}
	for n := len(directives); n > 0; n-- {
		p := change + "\n" + editContextHeading + "\n- " + strings.Join(directives[:n], "\n- ")
		if utf8.RuneCountInString(p) <= maxEditPromptChars {
			return p
		}
	}
	return change
}
