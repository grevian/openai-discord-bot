package bot

import (
	"context"
	"encoding/base64"
	"errors"
	"sync"
	"testing"
	"time"

	"openai-discord-bot/gallery"

	"github.com/bwmarrin/discordgo"
	"github.com/openai/openai-go/v3"
)

// fakeGallery is a galleryPublisher that records what it was offered.
type fakeGallery struct {
	mu        sync.Mutex
	got       []gallery.Submission
	hadTime   []bool
	cancelled []bool
	err       error
	// panicWith, when set, makes Publish panic with it.
	panicWith any
	// block, when set, makes Publish wait for it to be closed (or ctx to end).
	block chan struct{}
	// entered is closed-on-send when Publish starts.
	entered chan struct{}
}

func (f *fakeGallery) Publish(ctx context.Context, s gallery.Submission) error {
	if f.entered != nil {
		f.entered <- struct{}{}
	}
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
		}
	}
	if f.panicWith != nil {
		panic(f.panicWith)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := ctx.Deadline()
	f.hadTime = append(f.hadTime, ok)
	f.cancelled = append(f.cancelled, ctx.Err() != nil)
	f.got = append(f.got, s)
	return f.err
}

func (f *fakeGallery) submissions() []gallery.Submission {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]gallery.Submission(nil), f.got...)
}

func drawingBot(t *testing.T, g galleryPublisher) *testBot {
	t.Helper()
	tb := newTestBot(t)
	tb.bot.SetGallery(g)
	b64 := base64.StdEncoding.EncodeToString(pngBytes)
	tb.images.generate = func(context.Context, openai.ImageGenerateParams) (*openai.ImagesResponse, error) {
		return imageResponse(b64), nil
	}
	tb.images.edit = func(context.Context, openai.ImageEditParams) (*openai.ImagesResponse, error) {
		return imageResponse(b64), nil
	}
	return tb
}

func TestDrawIsOfferedToTheGallery(t *testing.T) {
	g := &fakeGallery{}
	tb := drawingBot(t, g)

	tb.bot.handleMessage(context.Background(), mention("draw me a picture of a cat"))
	tb.bot.galleryWG.Wait()

	got := g.submissions()
	if len(got) != 1 {
		t.Fatalf("submissions = %+v", got)
	}
	s := got[0]
	if s.GuildID != guildID || s.ImageKey != "guild-1/image-1" || s.Kind != gallery.KindGenerate {
		t.Errorf("submission = %+v", s)
	}
	// Only what the user asked for: not the drawing instructions, not the
	// bot's own mention.
	if s.Prompt != "  a cat" && s.Prompt != "a cat" {
		t.Errorf("prompt = %q", s.Prompt)
	}
}

func TestEditIsOfferedToTheGallery(t *testing.T) {
	g := &fakeGallery{}
	tb := drawingBot(t, g)
	const srcURL = "https://cdn.example/source.png"
	tb.imgStore.sources[srcURL] = pngBytes

	m := mention("edit: add a hat")
	m.Attachments = []*discordgo.MessageAttachment{{URL: srcURL, Filename: "source.png"}}
	tb.bot.handleMessage(context.Background(), m)
	tb.bot.galleryWG.Wait()

	got := g.submissions()
	if len(got) != 1 || got[0].Kind != gallery.KindEdit || got[0].Prompt != "add a hat" {
		t.Fatalf("submissions = %+v", got)
	}
}

// The gallery decides who is eligible, so DMs reach it with no guild ID. That
// empty ID is what makes gallery.Config.Eligible refuse them.
func TestDMsReachTheGalleryWithoutAGuild(t *testing.T) {
	g := &fakeGallery{}
	tb := drawingBot(t, g)
	m := mention("🎨 a cat")
	m.GuildID = ""
	tb.bot.handleMessage(context.Background(), m)
	tb.bot.galleryWG.Wait()

	got := g.submissions()
	if len(got) != 1 || got[0].GuildID != "" {
		t.Fatalf("submissions = %+v", got)
	}
	if (gallery.Config{Enabled: true}).Eligible(got[0].GuildID) {
		t.Error("a DM submission would be eligible")
	}
}

func TestGalleryFailureDoesNotAffectTheReply(t *testing.T) {
	g := &fakeGallery{err: errors.New("S3 is on fire")}
	tb := drawingBot(t, g)

	tb.bot.handleMessage(context.Background(), mention("🎨 a cat"))
	tb.bot.galleryWG.Wait()

	sent := tb.discord.sentMessages()
	if len(sent) != 1 || sent[0].Content != "a picture I drawed" {
		t.Fatalf("the user should get exactly the image, got %+v", sent)
	}
	if len(g.submissions()) != 1 {
		t.Error("the gallery was not tried")
	}
}

// The publish goroutine runs outside the handler's recover, so a panic in the
// gallery would otherwise crash the whole bot.
func TestGalleryPanicIsRecovered(t *testing.T) {
	g := &fakeGallery{panicWith: "gallery exploded"}
	tb := drawingBot(t, g)

	tb.bot.handleMessage(context.Background(), mention("🎨 a cat"))
	tb.bot.galleryWG.Wait() // would never return (or the process would die) without the recover

	if sent := tb.discord.sentMessages(); len(sent) != 1 || sent[0].Content != "a picture I drawed" {
		t.Fatalf("the user should still get the image, got %+v", sent)
	}
	// The bot keeps working afterwards.
	g.panicWith = nil
	tb.bot.handleMessage(context.Background(), mention("🎨 another cat"))
	tb.bot.galleryWG.Wait()
	if got := g.submissions(); len(got) != 1 {
		t.Errorf("later publish did not go through: %+v", got)
	}
}

func TestSlowGalleryDoesNotDelayTheReply(t *testing.T) {
	g := &fakeGallery{block: make(chan struct{}), entered: make(chan struct{}, 1)}
	tb := drawingBot(t, g)

	done := make(chan struct{})
	go func() {
		tb.bot.handleMessage(context.Background(), mention("🎨 a cat"))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the handler is waiting for the gallery")
	}
	if sent := tb.discord.sentMessages(); len(sent) != 1 {
		t.Fatalf("reply not sent: %+v", sent)
	}
	select {
	case <-g.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the gallery was never called")
	}
	close(g.block)
	tb.bot.galleryWG.Wait()
}

// The request context is cancelled the moment the handler returns, which must
// not abort the background publish; and the publish must still be bounded.
func TestGalleryPublishHasOwnDeadlineAndSurvivesRequestCancel(t *testing.T) {
	g := &fakeGallery{block: make(chan struct{})}
	tb := drawingBot(t, g)

	ctx, cancel := context.WithCancel(context.Background())
	tb.bot.handleMessage(ctx, mention("🎨 a cat"))
	cancel()
	close(g.block)
	tb.bot.galleryWG.Wait()

	if len(g.hadTime) != 1 || !g.hadTime[0] {
		t.Errorf("publish context had no deadline: %v", g.hadTime)
	}
	if g.cancelled[0] {
		t.Error("publish was cancelled along with the request")
	}
}

func TestGalleryPublishIsBounded(t *testing.T) {
	g := &fakeGallery{block: make(chan struct{})}
	tb := drawingBot(t, g)
	tb.bot.timeouts.Gallery = 50 * time.Millisecond

	tb.bot.handleMessage(context.Background(), mention("🎨 a cat"))
	done := make(chan struct{})
	go func() { tb.bot.galleryWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a hung gallery write was never abandoned")
	}
	if !g.cancelled[0] {
		t.Error("the publish context should have expired")
	}
}

func TestNothingIsOfferedWhenTheImageWasNotStored(t *testing.T) {
	g := &fakeGallery{}
	tb := drawingBot(t, g)
	tb.imgStore.storeErr = errors.New("AccessDenied")

	tb.bot.handleMessage(context.Background(), mention("🎨 a cat"))
	tb.bot.galleryWG.Wait()

	if got := g.submissions(); len(got) != 0 {
		t.Errorf("unstored image offered to the gallery: %+v", got)
	}
}

func TestNoGalleryConfiguredIsFine(t *testing.T) {
	tb := drawingBot(t, nil)
	tb.bot.gallery = nil
	tb.bot.handleMessage(context.Background(), mention("🎨 a cat"))
	if sent := tb.discord.sentMessages(); len(sent) != 1 {
		t.Fatalf("sent = %+v", sent)
	}
}

// DrainGallery waits for gallery publishes that are already running, so a deploy does
// not drop the image it was just handed.
func TestDrainGalleryWaitsForPublishes(t *testing.T) {
	g := &fakeGallery{block: make(chan struct{})}
	tb := drawingBot(t, g)
	tb.bot.messageCreate(nil, mention("🎨 a cat"))

	drained := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		drained <- tb.bot.DrainGallery(ctx)
	}()
	select {
	case err := <-drained:
		t.Fatalf("DrainGallery returned while a gallery publish was running: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(g.block)
	if err := <-drained; err != nil {
		t.Fatalf("DrainGallery: %v", err)
	}
	if len(g.submissions()) != 1 {
		t.Error("the publish did not complete")
	}
}

// A stuck publish must not hold shutdown past the gallery budget.
func TestDrainGalleryGivesUpAtBudget(t *testing.T) {
	g := &fakeGallery{block: make(chan struct{})}
	defer close(g.block)
	tb := drawingBot(t, g)
	tb.bot.messageCreate(nil, mention("🎨 a cat"))

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := tb.bot.DrainGallery(ctx); err == nil {
		t.Fatal("DrainGallery returned nil with a publish stuck")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("DrainGallery took %v", d)
	}
}

// With nothing in flight the drain steps return immediately.
func TestDrainIdleIsImmediate(t *testing.T) {
	tb := drawingBot(t, &fakeGallery{})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	start := time.Now()
	if err := tb.bot.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if err := tb.bot.DrainGallery(ctx); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Errorf("idle drain took %v", d)
	}
}

// A handler still running after Drain gave up can start a publish; the gallery
// step must wait for it rather than race its WaitGroup.Add.
func TestDrainGalleryWaitsForStragglerHandler(t *testing.T) {
	g := &fakeGallery{block: make(chan struct{}), entered: make(chan struct{}, 1)}
	tb := drawingBot(t, g)
	hctx, ok := tb.bot.beginRequest()
	if !ok {
		t.Fatal("request rejected")
	}

	expired, cancel := context.WithCancel(context.Background())
	cancel()
	if err := tb.bot.Drain(expired); err == nil {
		t.Fatal("Drain should time out with a handler running")
	}

	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		done <- tb.bot.DrainGallery(ctx)
	}()
	select {
	case err := <-done:
		t.Fatalf("DrainGallery returned with a handler still running: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	// The straggler finishes and starts a publish on its way out.
	tb.bot.publishToGallery(hctx, gallery.Submission{ImageKey: "k"})
	tb.bot.inflight.Done()
	<-g.entered
	select {
	case err := <-done:
		t.Fatalf("DrainGallery returned with a publish running: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(g.block)
	if err := <-done; err != nil {
		t.Fatalf("DrainGallery: %v", err)
	}
	if len(g.submissions()) != 1 {
		t.Error("the straggler's publish did not complete")
	}
}
