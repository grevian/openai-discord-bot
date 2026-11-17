package bot

import (
	"openai-discord-bot/bot/flags"
)

// flagRegistry holds every feature flag the bot knows. Each default must be
// the behaviour without the flag, so the bot is unchanged when flag storage
// is missing or unreachable. Change values by DM (see flagcmd.go).
var flagRegistry = flags.NewRegistry()

// EditThreadContext includes the thread's conversation in image edit
// prompts. The check is in handleImageEditMessage; the feature itself is not
// built yet, so for now the flag changes nothing but a span attribute.
var EditThreadContext = flagRegistry.Bool("edit_thread_context", false,
	"include thread conversation context in image edit prompts")

// NewFlags returns the flag cache for the bot's registry over backend. A nil
// backend means defaults only.
func NewFlags(backend flags.Backend) *flags.Flags {
	return flags.New(flagRegistry, backend)
}
