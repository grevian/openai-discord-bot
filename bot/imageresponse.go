package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

// leanImages wraps the SDK's image service and decodes each response itself.
//
// An image response is one JSON object that carries the whole PNG as a base64
// string (about 2-3 MB). The SDK reads the body into memory, then parses it
// with gjson, then again with encoding/json, and keeps the raw JSON of every
// field. That allocates about 20 MB per image. Decoding the body as a stream
// into only the fields the bot reads allocates about a third of that, which
// is most of the bot's peak memory when it draws.
//
// Requests, retries and error responses still go through the SDK: with a
// *http.Response destination it returns API errors as usual and only leaves a
// successful body unread.
type leanImages struct {
	svc imageAPI
}

func (l leanImages) Generate(ctx context.Context, body openai.ImageGenerateParams, opts ...option.RequestOption) (*openai.ImagesResponse, error) {
	var raw *http.Response
	if _, err := l.svc.Generate(ctx, body, append(opts, option.WithResponseBodyInto(&raw))...); err != nil {
		return nil, err
	}
	return decodeImagesBody(raw)
}

func (l leanImages) Edit(ctx context.Context, body openai.ImageEditParams, opts ...option.RequestOption) (*openai.ImagesResponse, error) {
	var raw *http.Response
	if _, err := l.svc.Edit(ctx, body, append(opts, option.WithResponseBodyInto(&raw))...); err != nil {
		return nil, err
	}
	return decodeImagesBody(raw)
}

// leanImagesResponse is the part of an image API response the bot reads.
// Usage is small, so it keeps the SDK's own type.
type leanImagesResponse struct {
	Created      int64                             `json:"created"`
	Background   openai.ImagesResponseBackground   `json:"background"`
	OutputFormat openai.ImagesResponseOutputFormat `json:"output_format"`
	Quality      openai.ImagesResponseQuality      `json:"quality"`
	Size         openai.ImagesResponseSize         `json:"size"`
	Usage        openai.ImagesResponseUsage        `json:"usage"`
	Data         []struct {
		B64JSON       string `json:"b64_json"`
		RevisedPrompt string `json:"revised_prompt"`
		URL           string `json:"url"`
	} `json:"data"`
}

// decodeImagesBody decodes a successful image response straight from its
// body, and closes the body.
func decodeImagesBody(raw *http.Response) (*openai.ImagesResponse, error) {
	if raw == nil || raw.Body == nil {
		return nil, fmt.Errorf("openai returned no response body")
	}
	defer func() { _ = raw.Body.Close() }()
	return decodeImagesResponse(raw.Body)
}

func decodeImagesResponse(r io.Reader) (*openai.ImagesResponse, error) {
	var lean leanImagesResponse
	if err := json.NewDecoder(r).Decode(&lean); err != nil {
		return nil, fmt.Errorf("error parsing image response json: %w", err)
	}
	resp := &openai.ImagesResponse{
		Created:      lean.Created,
		Background:   lean.Background,
		OutputFormat: lean.OutputFormat,
		Quality:      lean.Quality,
		Size:         lean.Size,
		Usage:        lean.Usage,
		Data:         make([]openai.Image, len(lean.Data)),
	}
	for i, d := range lean.Data {
		resp.Data[i] = openai.Image{B64JSON: d.B64JSON, RevisedPrompt: d.RevisedPrompt, URL: d.URL}
	}
	return resp, nil
}
