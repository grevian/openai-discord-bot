package bot

import (
	"openai-discord-bot/bot/flags"
)

// flagRegistry holds every feature flag the bot knows. Each default must be
// the behaviour without the flag, so the bot is unchanged when flag storage
// is missing or unreachable. Change values by DM (see flagcmd.go).
var flagRegistry = flags.NewRegistry()

// EditThreadContext includes the thread's conversation, or outside a thread
// the reply chain's earlier requests, in image edit prompts: a text model
// condenses them into a few directives (see editcontext.go). The check is in
// handleImageEditMessage.
var EditThreadContext = flagRegistry.Bool("edit_thread_context", false,
	"include thread or reply-chain context in image edit prompts")

// IntentDecisions asks the OpenAI Decisions API what a plain-text mention
// wants (draw, edit, summarize, help, feedback, idea) when no emoji or keyword
// trigger matched. The check is in handleMessage.
var IntentDecisions = flagRegistry.Bool("intent_decisions", false,
	"guess draw/edit/summarize/help/feedback from plain text with the OpenAI Decisions API")

// NewFlags returns the flag cache for the bot's registry over backend. A nil
// backend means defaults only.
func NewFlags(backend flags.Backend) *flags.Flags {
	return flags.New(flagRegistry, backend)
}

// RetryOpenAI clears degraded mode (entered on a fatal OpenAI error such as
// an exhausted quota or revoked key) so the next request calls OpenAI again.
// Set it after fixing the cause; it can stay on. Checked by isDegraded.
var RetryOpenAI = flagRegistry.Bool("retry_openai", false,
	"retry OpenAI after degraded mode (set once quota or key is fixed)")
