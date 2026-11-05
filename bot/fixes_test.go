package bot

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
)

// --- Image pipeline -------------------------------------------------------

func TestDispatchSendsFullImageWhenS3Fails(t *testing.T) {
	tb := newTestBot(t)
	tb.imgStore.storeErr = errors.New("AccessDenied")
	tb.images.generate = func(context.Context, openai.ImageGenerateParams) (*openai.ImagesResponse, error) {
		return imageResponse(base64.StdEncoding.EncodeToString(pngBytes)), nil
	}

	tb.bot.handleMessage(context.Background(), mention("🎨 a cat"))

	sent := tb.discord.sentMessages()
	if len(sent) != 1 {
		t.Fatalf("expected one Discord message, got %+v", sent)
	}
	if !bytes.Equal(sent[0].File, pngBytes) {
		t.Fatalf("Discord got %d bytes, want the full %d-byte image", len(sent[0].File), len(pngBytes))
	}
	for _, m := range tb.threads.addedMessages() {
		if m.Source == "Bot" {
			t.Errorf("no image URL should be recorded when the S3 upload failed, got %+v", m)
		}
	}
}

func TestDispatchDoesNotLeakUploadWhenDiscordFails(t *testing.T) {
	tb := newTestBot(t)
	tb.discord.sendComplexErr = errors.New("discord 500")
	tb.images.generate = func(context.Context, openai.ImageGenerateParams) (*openai.ImagesResponse, error) {
		return imageResponse(base64.StdEncoding.EncodeToString(pngBytes)), nil
	}

	done := make(chan struct{})
	go func() {
		tb.bot.handleMessage(context.Background(), mention("🎨 a cat"))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not return")
	}

	// The S3 upload must have finished by the time the handler returns: no
	// goroutine is left blocked on a pipe nobody reads.
	stored := tb.imgStore.storedImages()
	if len(stored) != 1 || !bytes.Equal(stored[0].Data, pngBytes) {
		t.Fatalf("expected the S3 upload to complete with the full image, got %+v", stored)
	}
	sent := tb.discord.sentMessages()
	if len(sent) != 1 || sent[0].Content != msgGeneric {
		t.Fatalf("expected the failure to be reported, got %+v", sent)
	}
}

// --- Empty image responses ------------------------------------------------

func TestEmptyImageResponsesAreErrors(t *testing.T) {
	cases := map[string]*openai.ImagesResponse{
		"nil response": nil,
		"no data":      {},
		"empty b64":    {Data: []openai.Image{{}}},
	}
	for name, resp := range cases {
		t.Run("generate/"+name, func(t *testing.T) {
			tb := newTestBot(t)
			tb.images.generate = func(context.Context, openai.ImageGenerateParams) (*openai.ImagesResponse, error) {
				return resp, nil
			}
			tb.bot.handleMessage(context.Background(), mention("🎨 a cat"))
			assertNoImageError(t, tb)
		})
		t.Run("edit/"+name, func(t *testing.T) {
			tb := newTestBot(t)
			const srcURL = "https://cdn.example/source.png"
			tb.imgStore.sources[srcURL] = pngBytes
			tb.images.edit = func(context.Context, openai.ImageEditParams) (*openai.ImagesResponse, error) {
				return resp, nil
			}
			m := mention("edit: add a hat")
			m.Attachments = []*discordgo.MessageAttachment{{URL: srcURL, Filename: "s.png"}}
			tb.bot.handleMessage(context.Background(), m)
			assertNoImageError(t, tb)
		})
	}
}

func assertNoImageError(t *testing.T, tb *testBot) {
	t.Helper()
	sent := tb.discord.sentMessages()
	if len(sent) != 1 || sent[0].File != nil || sent[0].Content != msgGeneric {
		t.Fatalf("expected a 'no image' error and no attachment, got %+v", sent)
	}
	if len(tb.imgStore.storedImages()) != 0 {
		t.Error("nothing should be uploaded to S3")
	}
}

// --- Panic recovery -------------------------------------------------------

func TestMessageCreateRecoversFromPanic(t *testing.T) {
	tb := newTestBot(t)
	tb.discord.self = nil // handleMessage dereferences the bot user
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic escaped the handler: %v", r)
		}
	}()
	tb.bot.messageCreate(nil, mention("hello"))
}

func TestReadyHandlerRecoversFromPanic(t *testing.T) {
	tb := newTestBot(t)
	tb.discord.onSend = func() { panic("boom") }
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic escaped the handler: %v", r)
		}
	}()
	tb.bot.ReadyHandler(nil, &discordgo.Ready{})
}

// --- Timeouts -------------------------------------------------------------

// TestEveryExternalCallHasADeadline drives each flow and checks that every
// call to Discord, OpenAI, DynamoDB, S3 and the image fetch carried a deadline.
func TestEveryExternalCallHasADeadline(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString(pngBytes)
	flows := map[string]func(tb *testBot){
		"chat in new thread": func(tb *testBot) {
			tb.responses.fn = func(context.Context, responses.ResponseNewParams) (*responses.Response, error) {
				return textResponse("hi"), nil
			}
			tb.bot.messageCreate(nil, mention("🧵 hello"))
		},
		"chat failure": func(tb *testBot) {
			tb.responses.fn = func(context.Context, responses.ResponseNewParams) (*responses.Response, error) {
				return nil, errors.New("nope")
			}
			tb.bot.messageCreate(nil, mention("hello"))
		},
		"draw": func(tb *testBot) {
			tb.images.generate = func(context.Context, openai.ImageGenerateParams) (*openai.ImagesResponse, error) {
				return imageResponse(b64), nil
			}
			tb.bot.messageCreate(nil, mention("🎨 a cat"))
		},
		"edit via fetched reference": func(tb *testBot) {
			const srcURL = "https://cdn.example/source.png"
			tb.imgStore.sources[srcURL] = pngBytes
			tb.discord.messages["old"] = &discordgo.Message{
				Author:      &discordgo.User{ID: botID},
				Attachments: []*discordgo.MessageAttachment{{URL: srcURL, Filename: "s.png"}},
			}
			tb.images.edit = func(context.Context, openai.ImageEditParams) (*openai.ImagesResponse, error) {
				return imageResponse(b64), nil
			}
			m := mention("edit: add a hat")
			m.MessageReference = &discordgo.MessageReference{ChannelID: channelID, MessageID: "old"}
			tb.bot.messageCreate(nil, m)
		},
		"ready and shutdown": func(tb *testBot) {
			tb.bot.ReadyHandler(nil, &discordgo.Ready{})
			tb.bot.Shutdown()
		},
	}
	for name, run := range flows {
		t.Run(name, func(t *testing.T) {
			tb := newTestBot(t)
			run(tb)
			calls, missing := tb.calls.snapshot()
			if len(calls) == 0 {
				t.Fatal("no external calls recorded")
			}
			if len(missing) != 0 {
				t.Errorf("calls without a deadline: %v (all calls: %v)", missing, calls)
			}
		})
	}
}

func TestHungOpenAICallTimesOut(t *testing.T) {
	tb := newTestBot(t)
	tb.bot.timeouts.OpenAIText = 50 * time.Millisecond
	tb.responses.fn = func(ctx context.Context, _ responses.ResponseNewParams) (*responses.Response, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	start := time.Now()
	tb.bot.messageCreate(nil, mention("hello"))
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("handler took %v despite a 50ms timeout", elapsed)
	}
	if got := tb.discord.sentMessages(); len(got) != 1 || got[0].Content != msgGeneric {
		t.Fatalf("expected the user to be told it failed, got %+v", got)
	}
}

func TestErrorIsReportedAfterRequestDeadline(t *testing.T) {
	tb := newTestBot(t)
	// The whole request runs out of time inside the OpenAI call; the error
	// notice must still be sent rather than failing on the expired context.
	tb.bot.timeouts.Request = 50 * time.Millisecond
	tb.responses.fn = func(ctx context.Context, _ responses.ResponseNewParams) (*responses.Response, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	tb.bot.messageCreate(nil, mention("hello"))
	if got := tb.discord.sentMessages(); len(got) != 1 || got[0].Content != msgGeneric {
		t.Fatalf("expected an error notice, got %+v", got)
	}
}

func TestDefaultTimeoutsArePositive(t *testing.T) {
	d := defaultTimeouts()
	for name, v := range map[string]time.Duration{
		"Request": d.Request, "OpenAIText": d.OpenAIText, "OpenAIImage": d.OpenAIImage,
		"Storage": d.Storage, "Discord": d.Discord,
	} {
		if v <= 0 {
			t.Errorf("%s timeout is %v", name, v)
		}
	}
	if d.OpenAIImage > d.Request || d.OpenAIText > d.Request {
		t.Error("per-call OpenAI timeouts should fit inside the request timeout")
	}
}

// --- Retries --------------------------------------------------------------

func TestEmptyCompletionIsNotRetried(t *testing.T) {
	tb := newTestBot(t)
	tb.responses.fn = func(context.Context, responses.ResponseNewParams) (*responses.Response, error) {
		return textResponse(""), nil
	}
	tb.bot.handleMessage(context.Background(), mention("hello"))
	if n := tb.responses.callCount(); n != 1 {
		t.Errorf("expected exactly one call, got %d", n)
	}
	if got := tb.discord.sentMessages(); len(got) != 1 || got[0].Content != msgGeneric {
		t.Errorf("unexpected messages %+v", got)
	}
}

// TestCompletionRetriesOnlyInTheSDK runs the real OpenAI client against a
// server that always fails with a retryable status, and counts HTTP attempts.
// The SDK retries twice by default, so there must be exactly 3 attempts, not
// 3 SDK attempts for each of 3 outer retries.
func TestCompletionRetriesOnlyInTheSDK(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.Header().Set("Retry-After-Ms", "1")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"overloaded","type":"server_error"}}`))
	}))
	defer srv.Close()

	client := openai.NewClient(option.WithBaseURL(srv.URL), option.WithAPIKey("test"))
	tb := newTestBot(t)
	tb.bot.responses = &client.Responses

	tb.bot.handleMessage(context.Background(), mention("hello"))

	if n := attempts.Load(); n != 3 {
		t.Errorf("expected 3 HTTP attempts (1 + 2 SDK retries), got %d", n)
	}
	if got := tb.discord.sentMessages(); len(got) != 1 || got[0].Content != msgGeneric {
		t.Errorf("unexpected messages %+v", got)
	}
}

// --- Review follow-ups ----------------------------------------------------

func TestArchivePanicDoesNotEscape(t *testing.T) {
	tb := newTestBot(t)
	tb.imgStore.onStore = func(context.Context) { panic("s3 sdk blew up") }
	tb.images.generate = func(context.Context, openai.ImageGenerateParams) (*openai.ImagesResponse, error) {
		return imageResponse(base64.StdEncoding.EncodeToString(pngBytes)), nil
	}
	// A panic in the S3 goroutine would crash the test binary; reaching the
	// assertions at all is the main check.
	tb.bot.messageCreate(nil, mention("🎨 a cat"))
	if sent := tb.discord.sentMessages(); len(sent) != 1 || !bytes.Equal(sent[0].File, pngBytes) {
		t.Fatalf("expected the image in Discord despite the S3 panic, got %+v", sent)
	}
}

func TestRequestDeadlineDefaultsToFiveMinutes(t *testing.T) {
	if got := defaultTimeouts().Request; got != 5*time.Minute {
		t.Fatalf("default request timeout = %v, want 5m", got)
	}

	tb := newTestBot(t)
	// No per-call limit, so the deadline the OpenAI call sees is the request's.
	tb.bot.timeouts.OpenAIText = 0
	var deadline time.Time
	var hasDeadline bool
	tb.responses.fn = func(ctx context.Context, _ responses.ResponseNewParams) (*responses.Response, error) {
		deadline, hasDeadline = ctx.Deadline()
		return textResponse("ok"), nil
	}
	start := time.Now()
	tb.bot.messageCreate(nil, mention("hello"))
	if !hasDeadline {
		t.Fatal("the request context has no deadline")
	}
	end := time.Now()
	if deadline.Before(start.Add(5*time.Minute)) || deadline.After(end.Add(5*time.Minute)) {
		t.Errorf("request deadline is %v after the start, want 5m", deadline.Sub(start))
	}
}

func TestHungS3UploadTimesOut(t *testing.T) {
	tb := newTestBot(t)
	tb.bot.timeouts.Storage = 50 * time.Millisecond
	tb.imgStore.onStore = func(ctx context.Context) { <-ctx.Done() }
	tb.images.generate = func(context.Context, openai.ImageGenerateParams) (*openai.ImagesResponse, error) {
		return imageResponse(base64.StdEncoding.EncodeToString(pngBytes)), nil
	}
	start := time.Now()
	tb.bot.messageCreate(nil, mention("🎨 a cat"))
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("handler took %v despite a 50ms storage timeout", elapsed)
	}
	if sent := tb.discord.sentMessages(); len(sent) != 1 || !bytes.Equal(sent[0].File, pngBytes) {
		t.Fatalf("expected the image in Discord, got %+v", sent)
	}
	if len(tb.imgStore.storedImages()) != 0 {
		t.Error("the timed-out upload must not be recorded as stored")
	}
	for _, m := range tb.threads.addedMessages() {
		if m.Source == "Bot" {
			t.Errorf("no image URL should be recorded after a timed-out upload, got %+v", m)
		}
	}
}

func TestStalledSourceImageDownloadTimesOut(t *testing.T) {
	tb := newTestBot(t)
	tb.bot.timeouts.Storage = 50 * time.Millisecond
	tb.imgStore.slowBody = true
	m := mention("edit: add a hat")
	m.Attachments = []*discordgo.MessageAttachment{{URL: "https://cdn.example/slow.png", Filename: "slow.png"}}

	start := time.Now()
	tb.bot.messageCreate(nil, m)
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("handler took %v despite a 50ms storage timeout", elapsed)
	}
	if len(tb.images.edited) != 0 {
		t.Error("no edit should be requested without the source image")
	}
	if sent := tb.discord.sentMessages(); len(sent) != 1 || sent[0].Content != msgGeneric {
		t.Fatalf("expected a read failure notice, got %+v", sent)
	}
}
