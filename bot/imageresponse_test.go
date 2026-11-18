package bot

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

// imageServer serves body with status for every request and counts the
// requests.
func imageServer(t testing.TB, status int, body []byte) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func imageClient(srv *httptest.Server) openai.Client {
	return openai.NewClient(option.WithAPIKey("test"), option.WithBaseURL(srv.URL+"/v1/"), option.WithMaxRetries(0))
}

func imagesBody(t testing.TB, b64 string) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"created":       1700000000,
		"output_format": "png",
		"size":          "1024x1024",
		"data":          []map[string]any{{"b64_json": b64, "revised_prompt": "a cat"}},
		"usage":         map[string]any{"input_tokens": 11, "output_tokens": 22, "total_tokens": 33},
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestLeanImagesDecodesGenerateAndEdit(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString([]byte("\x89PNG not really"))
	srv, hits := imageServer(t, http.StatusOK, imagesBody(t, b64))
	client := imageClient(srv)
	lean := leanImages{&client.Images}

	gen, err := lean.Generate(context.Background(), openai.ImageGenerateParams{Prompt: "a cat"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	edit, err := lean.Edit(context.Background(), openai.ImageEditParams{
		Prompt: "a hat",
		Image:  openai.ImageEditParamsImageUnion{OfFile: strings.NewReader("png")},
	})
	if err != nil {
		t.Fatalf("Edit: %v", err)
	}
	for name, resp := range map[string]*openai.ImagesResponse{"generate": gen, "edit": edit} {
		got, err := firstImageB64(resp)
		if err != nil || got != b64 {
			t.Errorf("%s: first image = %q, %v; want %q", name, got, err, b64)
		}
		if resp.Data[0].RevisedPrompt != "a cat" || resp.Created != 1700000000 || resp.Size != "1024x1024" {
			t.Errorf("%s: metadata not decoded: %+v", name, resp)
		}
		if resp.Usage.InputTokens != 11 || resp.Usage.OutputTokens != 22 || resp.Usage.TotalTokens != 33 {
			t.Errorf("%s: usage = %+v", name, resp.Usage)
		}
	}
	if hits.Load() != 2 {
		t.Errorf("expected 2 requests, got %d", hits.Load())
	}
}

// API errors must still come back as *openai.Error, so quota and key
// failures keep shutting the bot down (isFatalOpenAIError).
func TestLeanImagesKeepsAPIErrors(t *testing.T) {
	srv, _ := imageServer(t, http.StatusTooManyRequests, []byte(`{"error":{"message":"quota","type":"insufficient_quota","code":"insufficient_quota"}}`))
	client := imageClient(srv)

	_, err := leanImages{&client.Images}.Generate(context.Background(), openai.ImageGenerateParams{Prompt: "a cat"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !isFatalOpenAIError(err) {
		t.Errorf("expected a fatal OpenAI error, got %v", err)
	}
}

func TestLeanImagesRejectsBadJSON(t *testing.T) {
	srv, _ := imageServer(t, http.StatusOK, []byte(`{"data":[{"b64_json":`))
	client := imageClient(srv)

	if _, err := (leanImages{&client.Images}).Generate(context.Background(), openai.ImageGenerateParams{Prompt: "a cat"}); err == nil {
		t.Fatal("expected a decode error for a truncated body")
	}
}

// BenchmarkImageGenerate compares the SDK's own decoding with leanImages for
// a 2 MB image: run with -benchmem and compare B/op.
func BenchmarkImageGenerate(b *testing.B) {
	png := make([]byte, 2<<20)
	for i := range png {
		png[i] = byte(i * 7919 >> 3)
	}
	srv, _ := imageServer(b, http.StatusOK, imagesBody(b, base64.StdEncoding.EncodeToString(png)))
	client := imageClient(srv)
	for name, api := range map[string]imageAPI{"sdk": &client.Images, "lean": leanImages{&client.Images}} {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				resp, err := api.Generate(context.Background(), openai.ImageGenerateParams{Prompt: "a cat"})
				if err != nil {
					b.Fatal(err)
				}
				if _, err := firstImageB64(resp); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
