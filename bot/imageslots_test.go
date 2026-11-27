package bot

import (
	"context"
	"encoding/base64"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/openai/openai-go/v3"
)

// blockImages makes generate and edit wait for release, counting arrivals.
func blockImages(tb *testBot) (started *atomic.Int32, release chan struct{}) {
	started = &atomic.Int32{}
	release = make(chan struct{})
	resp := imageResponse(base64.StdEncoding.EncodeToString(pngBytes))
	tb.images.generate = func(context.Context, openai.ImageGenerateParams) (*openai.ImagesResponse, error) {
		started.Add(1)
		<-release
		return resp, nil
	}
	tb.images.edit = func(context.Context, openai.ImageEditParams) (*openai.ImagesResponse, error) {
		started.Add(1)
		<-release
		return resp, nil
	}
	return started, release
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("condition not reached")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestImageRequestsBeyondTheCapGetTheBusyReply(t *testing.T) {
	tb := newTestBot(t)
	tb.bot.imageSlots = newImageSlots(2)
	tb.bot.timeouts.ImageSlotWait = 30 * time.Millisecond
	tb.imgStore.sources["https://cdn.example/s.png"] = pngBytes
	started, release := blockImages(tb)

	var wg sync.WaitGroup
	run := func(m *discordgo.MessageCreate) {
		wg.Add(1)
		go func() { defer wg.Done(); tb.bot.handleMessage(context.Background(), m) }()
	}
	run(mention("draw me a picture of a cat"))
	run(mention("draw me a picture of a dog"))
	waitFor(t, func() bool { return started.Load() == 2 })

	// A draw and an edit both find the slots taken.
	em := mention("edit: add a hat")
	em.Attachments = []*discordgo.MessageAttachment{{URL: "https://cdn.example/s.png", Filename: "s.png"}}
	run(em)
	run(mention("draw me a picture of a bird"))
	waitFor(t, func() bool {
		n := 0
		for _, m := range tb.discord.sentMessages() {
			if m.Content == msgImagesBusy {
				n++
			}
		}
		return n == 2
	})
	if got := started.Load(); got != 2 {
		t.Errorf("only 2 image calls may run at once, %d started", got)
	}
	if n := len(tb.threads.addedMessages()); n != 2 {
		t.Errorf("refused requests must not be recorded, %d thread messages", n)
	}

	close(release)
	wg.Wait()

	// The slots are free again.
	tb.bot.handleMessage(context.Background(), mention("draw me a picture of a fish"))
	if got := started.Load(); got != 3 {
		t.Errorf("a later request should run, %d started", got)
	}
}

func TestImageRequestWaitsForASlot(t *testing.T) {
	tb := newTestBot(t)
	tb.bot.imageSlots = newImageSlots(1)
	tb.bot.timeouts.ImageSlotWait = 10 * time.Second
	started, release := blockImages(tb)

	var wg sync.WaitGroup
	for _, p := range []string{"draw me a picture of a cat", "draw me a picture of a dog"} {
		wg.Add(1)
		go func() { defer wg.Done(); tb.bot.handleMessage(context.Background(), mention(p)) }()
	}
	waitFor(t, func() bool { return started.Load() == 1 })
	time.Sleep(20 * time.Millisecond)
	if started.Load() != 1 {
		t.Fatal("the second request must wait")
	}
	release <- struct{}{}
	waitFor(t, func() bool { return started.Load() == 2 })
	close(release)
	wg.Wait()
	for _, m := range tb.discord.sentMessages() {
		if m.Content == msgImagesBusy {
			t.Error("a request that got a slot in time must not be told the bot is busy")
		}
	}
}

func TestImageSlotAcquireStopsWhenContextEnds(t *testing.T) {
	s := newImageSlots(1)
	rel, err := s.acquire(context.Background(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer rel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.acquire(ctx, time.Minute); err != context.Canceled {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	var nilSlots imageSlots
	if r, err := nilSlots.acquire(context.Background(), 0); err != nil || r == nil {
		t.Errorf("nil slots must not limit: %v", err)
	}
}
