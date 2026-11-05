package bot

import (
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"
)

// OpenAI model identifiers. Every model the bot calls is named here, so a
// model swap is a one-line change in this block.
const (
	// chatModel answers text prompts through the Responses API. It is a
	// reasoning model, so chatReasoningEffort must be set on every request.
	// Written as a string because openai-go v3.64.3 has no constant for it.
	chatModel           = "gpt-6-luna"
	chatReasoningEffort = shared.ReasoningEffortLow

	// imageGenerateModel draws pictures for "draw me ..." requests.
	imageGenerateModel = openai.ImageModelGPTImage2
	// imageEditModel edits an attached image for "edit: ..." requests.
	imageEditModel = openai.ImageModelGPTImage2
)
