package bot

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"openai-discord-bot/gallery"

	"github.com/bwmarrin/discordgo"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/segmentio/ksuid"
)

// replyChainBot returns a bot outside any thread, with edit_thread_context
// set to flagOn, whose image fakes answer every request with a valid image.
func replyChainBot(t *testing.T, flagOn bool) (*testBot, *fakeGallery) {
	t.Helper()
	g := &fakeGallery{}
	tb := drawingBot(t, g)
	ff := newFakeFlags()
	ff.bools[EditThreadContext.Name()] = flagOn
	tb.bot.flags = ff
	tb.imgStore.sources[editSrcURL] = pngBytes
	return tb, g
}

// chainMessages stores a reply chain in the fake Discord, oldest first: user
// requests and bot image replies alternate, each replying to the one before
// (by reference only, as the REST API returns older messages). It returns
// the newest bot reply.
func chainMessages(tb *testBot, requests ...string) *discordgo.Message {
	var prev *discordgo.Message
	for i, req := range requests {
		user := &discordgo.Message{
			ID: fmt.Sprintf("u%d", i), ChannelID: channelID,
			Author:  &discordgo.User{ID: userID},
			Content: "<@" + botID + "> " + req,
		}
		if prev != nil {
			user.MessageReference = prev.Reference()
		}
		bot := &discordgo.Message{
			ID: fmt.Sprintf("b%d", i), ChannelID: channelID,
			Author:           &discordgo.User{ID: botID},
			Content:          "here ya go",
			MessageReference: user.Reference(),
			Attachments: []*discordgo.MessageAttachment{{
				URL: editSrcURL, Filename: uploadName(ksuid.New().String()),
			}},
		}
		tb.discord.messages[user.ID] = user
		tb.discord.messages[bot.ID] = bot
		prev = bot
	}
	return prev
}

// replyTo is a new edit request replying to ref, with ref inlined as the
// gateway does.
func replyTo(ref *discordgo.Message, content string) *discordgo.MessageCreate {
	m := mention(content)
	m.ReferencedMessage = ref
	m.MessageReference = ref.Reference()
	return m
}

// An edit at the end of a reply chain is condensed from the chain's earlier
// requests, as an edit in a thread is from the thread.
func TestReplyChainFeedsTheCondensePass(t *testing.T) {
	tb, g := replyChainBot(t, true)
	tb.responses.fn = func(context.Context, responses.ResponseNewParams) (*responses.Response, error) {
		return textResponse("- a disco party\n- everyone wears a hat"), nil
	}
	last := chainMessages(tb, "draw me a picture of a disco party", "edit: everyone should wear a sweet hat")

	tb.bot.handleMessage(context.Background(), replyTo(last, "edit: add another dancer"))
	tb.bot.galleryWG.Wait()

	if n := tb.responses.callCount(); n != 1 {
		t.Fatalf("expected one condense call, got %d", n)
	}
	in := condenseInput(t, tb.responses.calls[0])
	for _, want := range []string{"a disco party", "everyone should wear a sweet hat", "add another dancer"} {
		if !strings.Contains(in, want) {
			t.Errorf("condense input is missing %q: %q", want, in)
		}
	}
	for _, unwanted := range []string{"here ya go", "<@" + botID + ">"} {
		if strings.Contains(in, unwanted) {
			t.Errorf("condense input must not include %q: %q", unwanted, in)
		}
	}
	if len(tb.images.edited) != 1 || !strings.Contains(tb.images.edited[0].Prompt, "everyone wears a hat") {
		t.Errorf("the directives should reach the edit prompt, got %+v", tb.images.edited)
	}
	got := g.submissions()
	if len(got) != 1 || len(got[0].Context) != 2 {
		t.Errorf("the directives should go to the gallery, got %+v", got)
	}
}

// With the flag off, a reply chain is not read: no fetches, no text call.
func TestReplyChainIgnoredWithFlagOff(t *testing.T) {
	tb, _ := replyChainBot(t, false)
	last := chainMessages(tb, "draw me a picture of a disco party", "edit: hats")

	tb.bot.handleMessage(context.Background(), replyTo(last, "edit: add another dancer"))

	if n := tb.responses.callCount(); n != 0 {
		t.Errorf("flag off must not call the text model, got %d calls", n)
	}
	if len(tb.discord.fetched) != 0 {
		t.Errorf("flag off must not walk the chain, fetched %v", tb.discord.fetched)
	}
	if len(tb.images.edited) != 1 {
		t.Fatalf("expected one Edit call, got %d", len(tb.images.edited))
	}
}

// The walk reads at most maxReplyChainMessages messages, keeping the newest.
func TestReplyChainWalkIsBounded(t *testing.T) {
	tb, _ := replyChainBot(t, true)
	tb.responses.fn = func(context.Context, responses.ResponseNewParams) (*responses.Response, error) {
		return textResponse("NONE"), nil
	}
	var reqs []string
	for i := range maxReplyChainMessages {
		reqs = append(reqs, fmt.Sprintf("edit: change %02d", i))
	}
	last := chainMessages(tb, reqs...)

	tb.bot.handleMessage(context.Background(), replyTo(last, "edit: one more"))

	if n := len(tb.discord.fetched); n > maxReplyChainMessages {
		t.Errorf("fetched %d messages, want at most %d", n, maxReplyChainMessages)
	}
	in := condenseInput(t, tb.responses.calls[0])
	newest := fmt.Sprintf("change %02d", maxReplyChainMessages-1)
	if !strings.Contains(in, newest) || strings.Contains(in, "change 00") {
		t.Errorf("the walk should keep the newest requests and stop before the oldest: %q", in)
	}
}

// A chain that loops back on itself ends the walk at the first message seen
// twice: each earlier message is fetched once and each request read once.
func TestReplyChainStopsOnCycle(t *testing.T) {
	tb, _ := replyChainBot(t, true)
	tb.responses.fn = func(context.Context, responses.ResponseNewParams) (*responses.Response, error) {
		return textResponse("NONE"), nil
	}
	last := chainMessages(tb, "draw me a picture of a cat", "edit: a hat")
	tb.discord.messages["u0"].MessageReference = last.Reference()

	tb.bot.handleMessage(context.Background(), replyTo(last, "edit: add a dog"))

	if len(tb.images.edited) != 1 {
		t.Fatalf("the edit should still run, got %d Edit calls", len(tb.images.edited))
	}
	// b1 is inlined; u1, b0 and u0 are fetched; u0's reference (b1) is seen.
	if got := tb.discord.fetched; len(got) != 3 {
		t.Errorf("fetched %v, want u1, b0 and u0 once each", got)
	}
	in := condenseInput(t, tb.responses.calls[0])
	for _, req := range []string{"a cat", "a hat"} {
		if n := strings.Count(in, req); n != 1 {
			t.Errorf("%q appears %d times in the condense input, want once: %q", req, n, in)
		}
	}
}

// A fetch that fails ends the walk; what was read so far is still used and
// the edit still runs.
func TestReplyChainStopsOnFetchFailure(t *testing.T) {
	tb, _ := replyChainBot(t, true)
	tb.responses.fn = func(context.Context, responses.ResponseNewParams) (*responses.Response, error) {
		return textResponse("NONE"), nil
	}
	last := chainMessages(tb, "draw me a picture of a cat", "edit: a hat")
	tb.discord.messages["u0"].MessageReference = &discordgo.MessageReference{ChannelID: channelID, MessageID: "gone"}

	tb.bot.handleMessage(context.Background(), replyTo(last, "edit: add a dog"))

	if len(tb.images.edited) != 1 {
		t.Fatalf("the edit should still run, got %d Edit calls", len(tb.images.edited))
	}
	if got := tb.discord.fetched; len(got) != 4 {
		t.Errorf("fetched %v, want u1, b0, u0 and the missing one", got)
	}
	in := condenseInput(t, tb.responses.calls[0])
	if !strings.Contains(in, "a cat") || !strings.Contains(in, "a hat") {
		t.Errorf("the requests read before the failure should be used: %q", in)
	}
}

// Each redraw in a reply chain records the one it was made from, so the whole
// chain is one stack in the gallery.
func TestReplyChainStacksInTheGallery(t *testing.T) {
	tb, g := replyChainBot(t, false)
	b64 := base64.StdEncoding.EncodeToString(pngBytes)
	tb.images.generate = func(context.Context, openai.ImageGenerateParams) (*openai.ImagesResponse, error) {
		return imageResponse(b64), nil
	}

	tb.bot.handleMessage(context.Background(), mention("draw me a picture of a disco party"))
	for _, req := range []string{"edit: hats", "edit: another dancer"} {
		tb.bot.galleryWG.Wait()
		sent := tb.discord.sentMessages()
		prev := &discordgo.Message{
			ID: "reply", ChannelID: channelID, Author: &discordgo.User{ID: botID},
			Attachments: []*discordgo.MessageAttachment{{URL: editSrcURL, Filename: sent[len(sent)-1].FileName}},
		}
		tb.bot.handleMessage(context.Background(), replyTo(prev, req))
	}
	tb.bot.galleryWG.Wait()

	got := g.submissions()
	if len(got) != 3 {
		t.Fatalf("submissions = %+v", got)
	}
	if got[0].Kind != gallery.KindGenerate || got[1].ParentKey != got[0].ImageKey || got[2].ParentKey != got[1].ImageKey {
		t.Errorf("each redraw should name the one before as its parent: %+v", got)
	}
}
