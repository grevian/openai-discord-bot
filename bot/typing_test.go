package bot

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
)

func (f *fakeDiscord) typingCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.typing)
}

// waitForTyping blocks until at least n typing calls were made.
func waitForTyping(t *testing.T, f *fakeDiscord, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for f.typingCount() < n {
		if time.Now().After(deadline) {
			t.Fatalf("only %d typing calls, wanted %d", f.typingCount(), n)
		}
		time.Sleep(time.Millisecond)
	}
}

// assertTypingStopped fails if typing calls keep arriving.
func assertTypingStopped(t *testing.T, f *fakeDiscord) {
	t.Helper()
	before := f.typingCount()
	time.Sleep(50 * time.Millisecond) // many refresh intervals
	if after := f.typingCount(); after != before {
		t.Fatalf("typing still being sent after handler returned: %d -> %d", before, after)
	}
}

func fastTypingBot(t *testing.T) *testBot {
	tb := newTestBot(t)
	tb.bot.timeouts.TypingRefresh = 5 * time.Millisecond
	return tb
}

func TestTypingRefreshedDuringSlowDrawAndStopsAfterSend(t *testing.T) {
	tb := fastTypingBot(t)
	b64 := base64.StdEncoding.EncodeToString(pngBytes)
	tb.images.generate = func(context.Context, openai.ImageGenerateParams) (*openai.ImagesResponse, error) {
		waitForTyping(t, tb.discord, 3) // generation outlasts several refreshes
		return imageResponse(b64), nil
	}
	tb.bot.handleMessage(context.Background(), mention("draw me a picture of a cat"))
	if len(tb.discord.sentMessages()) != 1 {
		t.Fatalf("expected the image to be sent, got %+v", tb.discord.sentMessages())
	}
	assertTypingStopped(t, tb.discord)
}

func TestTypingRefreshedDuringSlowChat(t *testing.T) {
	tb := fastTypingBot(t)
	tb.responses.fn = func(context.Context, responses.ResponseNewParams) (*responses.Response, error) {
		waitForTyping(t, tb.discord, 3)
		return textResponse("hi"), nil
	}
	tb.bot.handleMessage(context.Background(), mention("hello"))
	assertTypingStopped(t, tb.discord)
}

func TestTypingStopsBeforeErrorNotice(t *testing.T) {
	tb := fastTypingBot(t)
	tb.images.generate = func(context.Context, openai.ImageGenerateParams) (*openai.ImagesResponse, error) {
		waitForTyping(t, tb.discord, 2)
		return nil, errors.New("boom")
	}
	atNotice := -1
	tb.discord.mu.Lock()
	tb.discord.onSend = func() {
		atNotice = tb.discord.typingCount()
		time.Sleep(30 * time.Millisecond) // typing would refresh if still running
	}
	tb.discord.mu.Unlock()
	tb.bot.handleMessage(context.Background(), mention("draw me a picture of a cat"))
	if len(tb.discord.sentMessages()) != 1 {
		t.Fatalf("expected an error notice, got %+v", tb.discord.sentMessages())
	}
	if final := tb.discord.typingCount(); final != atNotice {
		t.Fatalf("typing sent while the error notice was posted: %d -> %d", atNotice, final)
	}
	assertTypingStopped(t, tb.discord)
}

func TestTypingStopsOnPanic(t *testing.T) {
	tb := fastTypingBot(t)
	tb.responses.fn = func(context.Context, responses.ResponseNewParams) (*responses.Response, error) {
		waitForTyping(t, tb.discord, 2)
		panic("handler exploded")
	}
	func() {
		defer func() { _ = recover() }()
		tb.bot.handleMessage(context.Background(), mention("hello"))
	}()
	assertTypingStopped(t, tb.discord)
}

func TestTypingStopsOnShutdown(t *testing.T) {
	tb := fastTypingBot(t)
	ctx, cancel := context.WithCancel(context.Background())
	_, stop := tb.bot.keepTyping(ctx, channelID)
	waitForTyping(t, tb.discord, 2)
	cancel() // Drain cancelling the handler context
	stop()   // must return: the goroutine has exited
	assertTypingStopped(t, tb.discord)
}

func TestStopTypingIsIdempotent(t *testing.T) {
	tb := fastTypingBot(t)
	ctx, stop := tb.bot.keepTyping(context.Background(), channelID)
	stop()
	stop()
	stopTypingFromContext(ctx)
	stopTypingFromContext(context.Background()) // no-op without a keepalive
	assertTypingStopped(t, tb.discord)
}

func TestTypingStopsBeforeChatReply(t *testing.T) {
	tb := fastTypingBot(t)
	tb.responses.fn = func(context.Context, responses.ResponseNewParams) (*responses.Response, error) {
		waitForTyping(t, tb.discord, 2)
		return textResponse("hi"), nil
	}
	var atSend, afterSleep int
	tb.discord.mu.Lock()
	tb.discord.onSend = func() {
		atSend = tb.discord.typingCount()
		time.Sleep(30 * time.Millisecond) // a slow POST
		afterSleep = tb.discord.typingCount()
	}
	tb.discord.mu.Unlock()
	tb.bot.handleMessage(context.Background(), mention("hello"))
	if len(tb.discord.sentMessages()) != 1 {
		t.Fatalf("expected a reply, got %+v", tb.discord.sentMessages())
	}
	if atSend != afterSleep {
		t.Fatalf("typing sent while the reply was being posted: %d -> %d", atSend, afterSleep)
	}
}

// Typing must still be on during the Discord upload, and off while the S3
// archive is still running afterwards.
func TestImageTypingCoversUploadAndStopsBeforeArchiveWait(t *testing.T) {
	tb := fastTypingBot(t)
	b64 := base64.StdEncoding.EncodeToString(pngBytes)
	tb.images.generate = func(context.Context, openai.ImageGenerateParams) (*openai.ImagesResponse, error) {
		return imageResponse(b64), nil
	}
	var uploadStart, uploadEnd int
	tb.discord.mu.Lock()
	tb.discord.onSendComplex = func() {
		uploadStart = tb.discord.typingCount()
		time.Sleep(40 * time.Millisecond) // a slow upload
		uploadEnd = tb.discord.typingCount()
	}
	tb.discord.mu.Unlock()
	var archiveStart, archiveEnd int
	tb.imgStore.mu.Lock()
	tb.imgStore.onStore = func(context.Context) {
		time.Sleep(100 * time.Millisecond) // let the upload finish and typing stop
		archiveStart = tb.discord.typingCount()
		time.Sleep(40 * time.Millisecond) // a slow S3 upload
		archiveEnd = tb.discord.typingCount()
	}
	tb.imgStore.mu.Unlock()

	tb.bot.handleMessage(context.Background(), mention("draw me a picture of a cat"))

	if uploadEnd <= uploadStart {
		t.Errorf("typing was not refreshed during the upload: %d -> %d", uploadStart, uploadEnd)
	}
	if archiveEnd != archiveStart {
		t.Errorf("typing still on during the S3 archive: %d -> %d", archiveStart, archiveEnd)
	}
}

// slowLinks is a linkExtractor that waits for typing refreshes before
// answering, like a slow Kagi call.
type slowLinks struct {
	t *testing.T
	f *fakeDiscord
}

func (s slowLinks) Extract(context.Context, string) (string, error) {
	waitForTyping(s.t, s.f, 3)
	return "page text", nil
}

// The Kagi call and the model call together can outlast one indicator, so
// typing must refresh through both and stop before the summary is posted.
func TestTypingCoversSummaryAndStopsBeforeReply(t *testing.T) {
	tb := fastTypingBot(t)
	tb.bot.links = slowLinks{t: t, f: tb.discord}
	var atModel int
	tb.responses.fn = func(context.Context, responses.ResponseNewParams) (*responses.Response, error) {
		atModel = tb.discord.typingCount()
		waitForTyping(t, tb.discord, atModel+2)
		return textResponse("a summary"), nil
	}
	var atSend, afterSleep int
	tb.discord.mu.Lock()
	tb.discord.onSendComplex = func() {
		atSend = tb.discord.typingCount()
		time.Sleep(30 * time.Millisecond) // a slow POST
		afterSleep = tb.discord.typingCount()
	}
	tb.discord.mu.Unlock()

	tb.bot.handleMessage(context.Background(), mention("📰 https://example.com/a"))

	if sent := tb.discord.sentMessages(); len(sent) != 1 || sent[0].Content != "a summary" {
		t.Fatalf("expected the summary, got %+v", sent)
	}
	if atModel < 3 {
		t.Errorf("typing was not refreshed during the Kagi call: %d calls", atModel)
	}
	if atSend != afterSleep {
		t.Fatalf("typing sent while the summary was being posted: %d -> %d", atSend, afterSleep)
	}
	assertTypingStopped(t, tb.discord)
}
