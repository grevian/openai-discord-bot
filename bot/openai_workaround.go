package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"

	gpt "github.com/sashabaranov/go-openai"
	"github.com/spf13/viper"
)

// openaiClientWrapper wraps *gpt.Client to work around a library bug where
// CreateEditImage does not include the "model" field in the multipart form body.
type openaiClientWrapper struct {
	*gpt.Client
	httpClient *http.Client
	authToken  string
	baseURL    string
}

func newOpenaiClientWrapper(client *gpt.Client) *openaiClientWrapper {
	return &openaiClientWrapper{
		Client:     client,
		httpClient: &http.Client{},
		authToken:  viper.GetString("OPENAI_AUTH_TOKEN"),
		baseURL:    "https://api.openai.com/v1",
	}
}

// CreateEditImage overrides the library method to properly include the model
// field in the multipart form body.
func (w *openaiClientWrapper) CreateEditImage(ctx context.Context, request gpt.ImageEditRequest) (gpt.ImageResponse, error) {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// Write the image file
	imagePart, err := writer.CreateFormFile("image", "image.png")
	if err != nil {
		return gpt.ImageResponse{}, fmt.Errorf("failed to create image form file: %w", err)
	}
	if _, err = io.Copy(imagePart, request.Image); err != nil {
		return gpt.ImageResponse{}, fmt.Errorf("failed to write image data: %w", err)
	}

	// Write optional mask
	if request.Mask != nil {
		maskPart, err := writer.CreateFormFile("mask", "mask.png")
		if err != nil {
			return gpt.ImageResponse{}, fmt.Errorf("failed to create mask form file: %w", err)
		}
		if _, err = io.Copy(maskPart, request.Mask); err != nil {
			return gpt.ImageResponse{}, fmt.Errorf("failed to write mask data: %w", err)
		}
	}

	fields := map[string]string{
		"prompt":          request.Prompt,
		"n":               strconv.Itoa(request.N),
		"size":            request.Size,
		"response_format": request.ResponseFormat,
		"model":           request.Model,
	}
	for k, v := range fields {
		if v == "" || v == "0" {
			continue
		}
		if err := writer.WriteField(k, v); err != nil {
			return gpt.ImageResponse{}, fmt.Errorf("failed to write field %s: %w", k, err)
		}
	}

	if err := writer.Close(); err != nil {
		return gpt.ImageResponse{}, fmt.Errorf("failed to close multipart writer: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.baseURL+"/images/edits", body)
	if err != nil {
		return gpt.ImageResponse{}, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+w.authToken)

	resp, err := w.httpClient.Do(req)
	if err != nil {
		return gpt.ImageResponse{}, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return gpt.ImageResponse{}, fmt.Errorf("error, status code: %d, status: %s, message: %s", resp.StatusCode, resp.Status, string(respBody))
	}

	var response gpt.ImageResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return gpt.ImageResponse{}, fmt.Errorf("failed to decode response: %w", err)
	}

	return response, nil
}
