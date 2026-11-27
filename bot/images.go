package bot

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"openai-discord-bot/bot/storage"
	"openai-discord-bot/gallery"

	"github.com/bwmarrin/discordgo"
	"github.com/openai/openai-go/v3"
	"github.com/segmentio/ksuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// maxEditSourceBytes is the size at which a source image is refused for an
// edit. openai-go documents the GPT image models (the ones edits here use)
// as taking a png, webp or jpg "less than 50MB" per image, so a source of
// this many bytes or more is too large. Sized images are checked against it,
// and bodies are read through a limit of the same size.
const maxEditSourceBytes = 50_000_000

// errSourceImageTooLarge is returned when the source image to edit is at or
// over maxEditSourceBytes. The message is shown to the user as is.
var errSourceImageTooLarge = newUserError("That image is too large for me to edit (the limit is 50MB). Big files scare me too. Please send a smaller one.",
	errors.New("source image too large"))

func (b *AIBot) handleImageMessage(ctx context.Context, responseChannel string, prompt string, clean bool, m *discordgo.MessageCreate) error {
	ctx, span := otel.GetTracerProvider().Tracer("AIBot").Start(ctx, "handleImageMessage")
	defer span.End()
	span.SetAttributes(attribute.String("intent", intentDraw.String()), attribute.Bool("clean", clean))

	// Held until the image is sent and archived: the bytes stay in memory
	// that long. Taken before anything is recorded, so a busy reply leaves
	// no trace in the thread.
	release, err := b.imageSlots.acquire(ctx, b.timeouts.ImageSlotWait)
	if err != nil {
		return err
	}
	defer release()

	// Record the prompt to our thread context
	err = b.recordUser(ctx, responseChannel, m, prompt)
	if err != nil {
		return fmt.Errorf("failed to record drawing prompt: %w", err)
	}

	// The image API has no system-prompt slot, so the scene and the danbot
	// art style share one prompt (scene first, then the style block).
	// A clean request sends the user's text alone.
	fullPrompt := prompt
	if !clean {
		fullPrompt = buildDrawPrompt(prompt, b.drawingInstructions)
	}

	// Request the image(s) from openAI
	imageRequest := openai.ImageGenerateParams{
		Prompt:           fullPrompt,
		N:                openai.Int(1),
		SafetyIdentifier: openai.String(m.Author.ID),
		Size:             openai.ImageGenerateParamsSize1024x1024,
		Model:            imageGenerateModel,
	}
	span.SetAttributes(
		attribute.String("model", string(imageRequest.Model)),
		attribute.String("size", string(imageRequest.Size)),
	)
	openaiCtx, cancel := withTimeout(ctx, b.timeouts.OpenAIImage)
	responseImage, err := b.imagesGenerate(openaiCtx, imageRequest)
	cancel()
	if err != nil {
		if isFatalOpenAIError(err) {
			b.handleFatalOpenAIError(ctx, err)
		}
		return fmt.Errorf("failed to get image from openai: %w", err)
	}
	b64, err := firstImageB64(responseImage)
	if err != nil {
		return err
	}

	if err := b.dispatchGeneratedImage(ctx, span, responseChannel, b64, cleanReply("a picture I drawed", clean), prompt, gallery.KindGenerate, clean, editOrigin{}, m); err != nil {
		return err
	}

	span.SetStatus(codes.Ok, "Success")
	return nil
}

// firstImageB64 returns the first image in an image API response, or an error
// when the response carries none.
func firstImageB64(resp *openai.ImagesResponse) (string, error) {
	if resp == nil || len(resp.Data) == 0 || resp.Data[0].B64JSON == "" {
		return "", errors.New("openai returned no image")
	}
	return resp.Data[0].B64JSON, nil
}

// dispatchGeneratedImage takes a base64 PNG from the OpenAI image API, sends
// it to Discord as a reply attachment, and concurrently uploads a copy to S3 +
// records the resulting URL in thread context. Shared between the generation
// and edit paths.
//
// The image is decoded once into memory (about 1-2 MB) and both uploads read
// their own reader over those bytes, so an S3 failure cannot truncate what
// Discord receives, and a Discord failure cannot block the S3 upload. Both
// finish before this returns.
func (b *AIBot) dispatchGeneratedImage(ctx context.Context, span trace.Span, responseChannel, b64, content, prompt string, kind gallery.Kind, clean bool, origin editOrigin, m *discordgo.MessageCreate) error {
	logger := slog.Default().WithGroup("dispatchGeneratedImage")

	image, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return fmt.Errorf("invalid base64 image from openai: %w", err)
	}
	logger.DebugContext(ctx, "image retrieval", slog.Int("image_length", len(image)))

	// The Discord upload is named after the archive id, so a later reply to
	// this message can say which archived image it edits (see findEditSource).
	id := newImageID()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		// This goroutine is outside the handler's recover, so it needs its own.
		defer recoverHandler("archiveImage")
		b.archiveImage(ctx, span, responseChannel, m.GuildID, id, image, prompt, kind, clean, origin)
	}()

	opt, cancel := b.discordCtx(ctx)
	_, err = b.discord.ChannelMessageSendComplex(responseChannel, &discordgo.MessageSend{
		Content:         content,
		Reference:       m.Reference(),
		AllowedMentions: defaultAllowedMentions(),
		File: &discordgo.File{
			Name:        uploadName(id),
			ContentType: "image/png",
			Reader:      bytes.NewReader(image),
		},
	}, opt)
	cancel()
	stopTypingFromContext(ctx)
	wg.Wait()
	if err != nil {
		return fmt.Errorf("failed to send embedded image to discord: %w", err)
	}
	return nil
}

// archiveImage stores a copy of a generated image in S3 and records its public
// URL in the thread context. Failures are recorded and logged, not returned:
// the user already has the image in Discord. Once the image is stored it is
// also offered to the public gallery.
func (b *AIBot) archiveImage(ctx context.Context, span trace.Span, responseChannel, guildID, id string, image []byte, prompt string, kind gallery.Kind, clean bool, origin editOrigin) {
	logger := slog.Default().WithGroup("dispatchGeneratedImage")

	storeCtx, cancel := withTimeout(ctx, b.timeouts.Storage)
	imageKey, err := b.imageStorage.StoreImage(storeCtx, guildID, id, bytes.NewReader(image), int64(len(image)))
	cancel()
	if err != nil {
		span.RecordError(err)
		logger.ErrorContext(ctx, "failed to store a copy of the image in S3", slog.Any("error", err))
		return
	}

	b.publishToGallery(ctx, gallery.Submission{
		GuildID:   guildID,
		ImageKey:  imageKey,
		Prompt:    b.galleryPrompt(prompt),
		Kind:      kind,
		Clean:     clean,
		ParentKey: origin.parentKey,
		Context:   origin.directives,
	})

	imageUrl := fmt.Sprintf("%s/%s", b.imageBaseURL, imageKey)

	if err := b.recordBot(ctx, responseChannel, imageUrl); err != nil {
		span.RecordError(err)
		logger.ErrorContext(ctx, "failed to record image URL in thread context", slog.Any("error", err))
	}
}

// publishToGallery offers a stored image to the gallery in the background.
// It is best-effort: it never blocks or fails the reply, has its own deadline
// (the request context is cancelled as soon as the handler returns), and logs
// its failures.
func (b *AIBot) publishToGallery(ctx context.Context, s gallery.Submission) {
	if b.gallery == nil {
		return
	}
	b.galleryWG.Add(1)
	go func() {
		defer b.galleryWG.Done()
		defer recoverHandler("publishToGallery")
		pubCtx, cancel := withTimeout(context.WithoutCancel(ctx), b.timeouts.Gallery)
		defer cancel()
		if err := b.gallery.Publish(pubCtx, s); err != nil {
			slog.Default().WithGroup("gallery").ErrorContext(ctx, "failed to publish image to the gallery",
				slog.String("image_key", s.ImageKey), slog.Any("error", err))
		}
	}()
}

// editOrigin is what an edited image came from: the archive key of the image
// it was edited from ("" when unknown) and the condensed thread directives
// that went into its prompt. The zero value is a fresh drawing.
type editOrigin struct {
	parentKey  string
	directives []string
}

// newImageID returns a fresh archive id for a generated image.
func newImageID() string { return ksuid.New().String() }

// uploadName is the file name of a generated image sent to Discord. It
// carries the image's archive id; parentKeyFromUpload reads it back.
func uploadName(id string) string { return "danbot-" + id + ".png" }

// parentKeyFromUpload returns the archive key ("<guild>/<id>") of one of the
// bot's own Discord uploads, from its file name, or "" when the name carries
// no archive id (such as replies from before uploads were named this way).
// Only the bot's own messages may be passed: a user can name a file anything.
func parentKeyFromUpload(guildID, filename string) string {
	id, ok := strings.CutPrefix(filename, "danbot-")
	if !ok || guildID == "" {
		return ""
	}
	id, ok = strings.CutSuffix(id, ".png")
	if !ok {
		return ""
	}
	if _, err := ksuid.Parse(id); err != nil {
		return ""
	}
	return guildID + "/" + id
}

// galleryPrompt removes the bot's own mention from a prompt before it is
// published.
func (b *AIBot) galleryPrompt(prompt string) string {
	self := b.discord.BotUser()
	if self == nil {
		return prompt
	}
	return strings.NewReplacer("<@"+self.ID+">", "", "<@!"+self.ID+">", "").Replace(prompt)
}

// handleImageEditMessage takes a prompt and a URL to an existing image and asks
// OpenAI's image edit endpoint to produce a modified version, then ships it the
// same way handleImageMessage does for fresh generations.
func (b *AIBot) handleImageEditMessage(ctx context.Context, responseChannel, prompt string, source editSource, clean bool, threadContext []storage.ConversationMessage, m *discordgo.MessageCreate) error {
	logger := slog.Default().WithGroup("handleImageEditMessage")
	sourceURL := source.url

	ctx, span := otel.GetTracerProvider().Tracer("AIBot").Start(ctx, "handleImageEditMessage")
	defer span.End()
	span.SetAttributes(attribute.String("source_url", sourceURL),
		attribute.String("intent", intentEdit.String()), attribute.Bool("clean", clean))
	// With edit_thread_context on, the thread's earlier messages are condensed
	// into directives for the prompt. Off, the request is the plain one.
	useThreadContext := b.flags.Enabled(ctx, EditThreadContext)
	span.SetAttributes(attribute.Bool("flag.edit_thread_context", useThreadContext))

	// Held until the edited image is sent and archived (see the draw path).
	release, err := b.imageSlots.acquire(ctx, b.timeouts.ImageSlotWait)
	if err != nil {
		return err
	}
	defer release()

	if err := b.recordEdit(ctx, responseChannel, m, prompt, sourceURL); err != nil {
		return fmt.Errorf("failed to record edit prompt: %w", err)
	}

	srcBytes, err := b.fetchSourceImage(ctx, sourceURL)
	if err != nil {
		return err
	}
	srcContentType, srcFilename := detectImageType(srcBytes)
	logger.DebugContext(ctx, "source image fetched",
		slog.Int("bytes", len(srcBytes)),
		slog.String("content_type", srcContentType))
	if srcContentType == "" {
		return newUserError("That image isn't a supported format. Please use png, jpeg or webp. Nothing else ever works out for me anyway.", fmt.Errorf("source image is not a supported format (png/jpeg/webp)"))
	}

	// A default edit carries the danbot drawing style as well as the keep
	// list; a clean one sends neither (see below).
	guidance := editGuidance(b.drawingInstructions, b.editInstructions)
	fullPrompt := buildEditPrompt(prompt, guidance)
	var directives []string
	if clean {
		fullPrompt = prompt
	}
	if useThreadContext {
		// threadContext was loaded before this edit was recorded, so it holds
		// only earlier messages (and is empty outside a thread). Outside a
		// thread, a reply chain of requests serves the same purpose.
		history := threadContext
		if len(history) == 0 && source.reply != nil {
			history = b.replyChain(ctx, source.reply)
			span.SetAttributes(attribute.Int("edit_context.reply_chain", len(history)))
		}
		directives = b.editDirectives(ctx, history, prompt)
		span.SetAttributes(attribute.Int("edit_context.directives", len(directives)))
		if clean {
			fullPrompt = buildCleanEditPromptWithContext(prompt, directives)
		} else {
			fullPrompt = buildEditPromptWithContext(prompt, directives, guidance)
		}
	}

	// The OpenAI multipart encoder needs a recognizable filename + content type;
	// without it the form part is sent as application/octet-stream and the Edit
	// endpoint rejects it. openai.File wires both onto the io.Reader.
	editRequest := openai.ImageEditParams{
		Image:            openai.ImageEditParamsImageUnion{OfFile: openai.File(bytes.NewReader(srcBytes), srcFilename, srcContentType)},
		Prompt:           fullPrompt,
		N:                openai.Int(1),
		SafetyIdentifier: openai.String(m.Author.ID),
		Size:             openai.ImageEditParamsSize1024x1024,
		Model:            imageEditModel,
	}
	span.SetAttributes(
		attribute.String("model", string(editRequest.Model)),
		attribute.String("size", string(editRequest.Size)),
	)

	openaiCtx, cancel := withTimeout(ctx, b.timeouts.OpenAIImage)
	responseImage, err := b.imagesEdit(openaiCtx, editRequest)
	cancel()
	if err != nil {
		if isFatalOpenAIError(err) {
			b.handleFatalOpenAIError(ctx, err)
		}
		return fmt.Errorf("failed to edit image via openai: %w", err)
	}
	b64, err := firstImageB64(responseImage)
	if err != nil {
		return err
	}

	if err := b.dispatchGeneratedImage(ctx, span, responseChannel, b64, cleanReply("here ya go", clean), prompt, gallery.KindEdit, clean, editOrigin{parentKey: source.parentKey, directives: directives}, m); err != nil {
		return err
	}

	span.SetStatus(codes.Ok, "Success")
	return nil
}

// fetchSourceImage downloads the image to edit. The deadline covers both the
// request and reading the body. A source at or over maxEditSourceBytes is
// refused: first by its reported length when there is one, then by reading at
// most maxEditSourceBytes, so a body with no length cannot grow without bound.
func (b *AIBot) fetchSourceImage(ctx context.Context, sourceURL string) ([]byte, error) {
	fetchCtx, cancel := withTimeout(ctx, b.timeouts.Storage)
	defer cancel()

	src, length, err := b.imageStorage.GetImageFromURL(fetchCtx, sourceURL)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch source image: %w", err)
	}
	defer func() { _ = src.Close() }()

	// length is -1 when the response had no Content-Length.
	if length >= maxEditSourceBytes {
		return nil, errSourceImageTooLarge
	}

	// The Edit endpoint sends the image as multipart form data, which means the
	// SDK reads the whole body up front — buffer it here so we can pass an
	// io.Reader that won't get drained partway through.
	srcBytes, err := io.ReadAll(io.LimitReader(src, maxEditSourceBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to read source image: %w", err)
	}
	// Reading stopped at the limit, so a full buffer means the body was at
	// least that big.
	if len(srcBytes) >= maxEditSourceBytes {
		return nil, errSourceImageTooLarge
	}
	return srcBytes, nil
}

// editSource is the image an edit starts from.
type editSource struct {
	// url is where to fetch it; "" when there is none.
	url string
	// clean is set for one of the bot's own replies that carries the clean
	// marker. An attached image has no marker, so the user adds 🫧 again.
	clean bool
	// parentKey is the archive key of the image, when it is one of the bot's
	// own replies whose upload name carries it; "" otherwise.
	parentKey string
	// reply is the bot's own message the image came from, when the edit
	// replies to it; nil for an attached image. Its reply chain can stand in
	// for thread context (see replyChain).
	reply *discordgo.Message
}

// findEditSource finds the image an edit starts from. It prefers an image
// attached to the triggering message, then falls back to one attached to the
// message it replies to, if the bot wrote that message. Only that bot reply
// can be clean (it carries the marker) or name its archived image (its upload
// name, see parentKeyFromUpload). The zero editSource means no candidate.
func (b *AIBot) findEditSource(ctx context.Context, m *discordgo.MessageCreate) editSource {
	for _, att := range m.Attachments {
		if isImageAttachment(att) {
			return editSource{url: att.URL}
		}
	}

	ref := m.ReferencedMessage
	if ref == nil && m.MessageReference != nil && m.MessageReference.MessageID != "" {
		// The gateway doesn't always inline ReferencedMessage — older messages
		// or cache misses fall back to an explicit fetch.
		opt, cancel := b.discordCtx(ctx)
		fetched, err := b.discord.ChannelMessage(m.MessageReference.ChannelID, m.MessageReference.MessageID, opt)
		cancel()
		if err == nil {
			ref = fetched
		}
	}
	if ref == nil || ref.Author == nil || ref.Author.ID != b.discord.BotUser().ID {
		return editSource{}
	}
	for _, att := range ref.Attachments {
		if isImageAttachment(att) {
			// A reply is in the same guild as the message it answers, and a
			// fetched message has no guild ID of its own.
			return editSource{url: att.URL, clean: hasCleanEmoji(ref.Content), parentKey: parentKeyFromUpload(m.GuildID, att.Filename), reply: ref}
		}
	}
	return editSource{}
}

// detectImageType sniffs the leading bytes of an image and returns the MIME
// type + a matching filename for the formats OpenAI's image edit endpoint
// accepts (png/jpeg/webp). Returns "", "" for anything else.
func detectImageType(b []byte) (contentType, filename string) {
	sniffed := http.DetectContentType(b)
	switch sniffed {
	case "image/png":
		return sniffed, "source.png"
	case "image/jpeg":
		return sniffed, "source.jpg"
	case "image/webp":
		return sniffed, "source.webp"
	}
	return "", ""
}

func isImageAttachment(att *discordgo.MessageAttachment) bool {
	if att == nil {
		return false
	}
	if strings.HasPrefix(att.ContentType, "image/") {
		return true
	}
	lower := strings.ToLower(att.Filename)
	return strings.HasSuffix(lower, ".png") ||
		strings.HasSuffix(lower, ".jpg") ||
		strings.HasSuffix(lower, ".jpeg") ||
		strings.HasSuffix(lower, ".webp")
}
