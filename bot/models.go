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
	chatModel           = shared.ChatModelGPT6Luna
	chatReasoningEffort = shared.ReasoningEffortLow

	// imageGenerateModel draws pictures for "draw me ..." requests. openai-go
	// v3.91.1 has no constant for the 2.6 Flare model, so it is named directly.
	imageGenerateModel = openai.ImageModel("gpt-image-2.6-flare")
	// imageEditModel edits an attached image for "edit: ..." requests.
	imageEditModel = openai.ImageModel("gpt-image-2.6-flare")
)
