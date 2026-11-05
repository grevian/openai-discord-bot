package bot

import (
	"context"
	"io"

	"openai-discord-bot/bot/storage"
	"openai-discord-bot/gallery"

	"github.com/bwmarrin/discordgo"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
)

// The interfaces below are the bot's view of its external dependencies. They
// are declared here, where they are consumed, so that tests can substitute
// in-memory fakes for Discord, OpenAI, DynamoDB and S3.

// threadStore persists and loads per-thread conversation history.
// *storage.Storage satisfies it.
type threadStore interface {
	GetThread(ctx context.Context, threadId string) ([]storage.ConversationMessage, error)
	AddThreadMessage(ctx context.Context, threadId string, messageSource string, message string) error
}

// imageStore stores generated images and fetches source images for edits.
// *storage.ImageStorage satisfies it.
type imageStore interface {
	StoreImage(ctx context.Context, groupId string, reader io.Reader, contentLength int64) (string, error)
	GetImageFromURL(ctx context.Context, URL string) (io.ReadCloser, int64, error)
}

// galleryPublisher adds generated images to the public gallery.
// *gallery.Gallery satisfies it. It decides eligibility itself (DMs and
// excluded guilds are ignored), so the bot passes every image.
type galleryPublisher interface {
	Publish(ctx context.Context, s gallery.Submission) error
}

// imageAPI is the subset of the OpenAI image service the bot uses.
// *openai.ImageService satisfies it.
type imageAPI interface {
	Generate(ctx context.Context, body openai.ImageGenerateParams, opts ...option.RequestOption) (*openai.ImagesResponse, error)
	Edit(ctx context.Context, body openai.ImageEditParams, opts ...option.RequestOption) (*openai.ImagesResponse, error)
}

// responsesAPI is the subset of the OpenAI responses service the bot uses.
// *responses.ResponseService satisfies it.
type responsesAPI interface {
	New(ctx context.Context, body responses.ResponseNewParams, opts ...option.RequestOption) (*responses.Response, error)
}

// discordAPI is the subset of the Discord session the bot uses: the REST
// calls, plus the two reads it makes from the gateway state cache.
type discordAPI interface {
	// BotUser returns the bot's own user, from the session state.
	BotUser() *discordgo.User
	// StateChannel looks a channel up in the session's state cache.
	StateChannel(channelID string) (*discordgo.Channel, error)

	ChannelTyping(channelID string, options ...discordgo.RequestOption) error
	ChannelMessage(channelID, messageID string, options ...discordgo.RequestOption) (*discordgo.Message, error)
	ChannelMessageSend(channelID string, content string, options ...discordgo.RequestOption) (*discordgo.Message, error)
	ChannelMessageSendComplex(channelID string, data *discordgo.MessageSend, options ...discordgo.RequestOption) (*discordgo.Message, error)
	MessageThreadStartComplex(channelID, messageID string, data *discordgo.ThreadStart, options ...discordgo.RequestOption) (*discordgo.Channel, error)
}

// sessionDiscord adapts a *discordgo.Session to discordAPI. The REST methods
// come from the embedded session; only the state reads need wrapping.
type sessionDiscord struct {
	*discordgo.Session
}

func (s sessionDiscord) BotUser() *discordgo.User {
	return s.State.User
}

func (s sessionDiscord) StateChannel(channelID string) (*discordgo.Channel, error) {
	return s.State.Channel(channelID)
}

// Compile-time checks that the production types satisfy the interfaces.
var (
	_ threadStore      = (*storage.Storage)(nil)
	_ imageStore       = (*storage.ImageStorage)(nil)
	_ galleryPublisher = (*gallery.Gallery)(nil)
	_ imageAPI         = (*openai.ImageService)(nil)
	_ responsesAPI     = (*responses.ResponseService)(nil)
	_ discordAPI       = sessionDiscord{}
)
