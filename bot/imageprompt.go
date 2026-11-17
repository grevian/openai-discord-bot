package bot

import (
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
