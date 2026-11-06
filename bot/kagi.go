package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

const (
	// kagiExtractURL is Kagi's Extract API endpoint. It fetches a page on
	// Kagi's side and returns its readable text, so the bot never fetches the
	// URL itself.
	kagiExtractURL = "https://kagi.com/api/v1/extract"
	// kagiPageTimeout is the extraction timeout, in seconds, that Kagi is
	// asked to honour for one page. The HTTP call itself is bounded by
	// timeouts.LinkExtract, which must be longer.
	kagiPageTimeout = 15
	// maxKagiResponseBytes caps how much of Kagi's response body is read.
	maxKagiResponseBytes = 4 << 20
)

// errLinkUnreadable means Kagi answered but could not extract readable text
// from the page (blocked, empty, not found, unsupported content and so on).
var errLinkUnreadable = errors.New("kagi could not extract the page")

// kagiClient is a minimal client for the Kagi Extract API.
type kagiClient struct {
	endpoint string
	apiKey   string
	http     *http.Client
}

// newKagiClient returns a client that authenticates with apiKey.
func newKagiClient(apiKey string) *kagiClient {
	return &kagiClient{
		endpoint: kagiExtractURL,
		apiKey:   apiKey,
		http: &http.Client{
			Transport: otelhttp.NewTransport(http.DefaultTransport),
			// A backstop only: callers bound each call with a context.
			Timeout: 2 * time.Minute,
		},
	}
}

type kagiExtractRequest struct {
	Pages   []kagiPage `json:"pages"`
	Timeout int        `json:"timeout,omitempty"`
}

type kagiPage struct {
	URL string `json:"url"`
}

type kagiExtractResponse struct {
	Meta struct {
		Trace string `json:"trace"`
		MS    int    `json:"ms"`
	} `json:"meta"`
	Data []struct {
		URL      string  `json:"url"`
		Markdown *string `json:"markdown"`
		Error    string  `json:"error"`
	} `json:"data"`
	// Errors is the per-page error list of a 200 response; Error is the
	// request-level list of a 4xx response.
	Errors []kagiError `json:"errors"`
	Error  []kagiError `json:"error"`
}

type kagiError struct {
	Code    string `json:"code"`
	URL     string `json:"url"`
	Message string `json:"message"`
}

// kagiAPIError is a non-200 answer from Kagi. It never carries the API key.
type kagiAPIError struct {
	StatusCode int
	Code       string
	Message    string
	Trace      string
}

func (e *kagiAPIError) Error() string {
	return fmt.Sprintf("kagi extract: HTTP %d %s: %s (trace %s)", e.StatusCode, e.Code, e.Message, e.Trace)
}

// Extract asks Kagi for the readable text of pageURL. It returns
// errLinkUnreadable (wrapped) when Kagi answered but had no text for the
// page, and a *kagiAPIError for a non-200 answer.
func (k *kagiClient) Extract(ctx context.Context, pageURL string) (string, error) {
	body, err := json.Marshal(kagiExtractRequest{
		Pages:   []kagiPage{{URL: pageURL}},
		Timeout: kagiPageTimeout,
	})
	if err != nil {
		return "", fmt.Errorf("kagi extract: encoding request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, k.endpoint, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("kagi extract: building request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+k.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := k.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("kagi extract: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxKagiResponseBytes))
	if err != nil {
		return "", fmt.Errorf("kagi extract: reading response: %w", err)
	}
	var parsed kagiExtractResponse
	decodeErr := json.Unmarshal(raw, &parsed)

	if resp.StatusCode != http.StatusOK {
		apiErr := &kagiAPIError{StatusCode: resp.StatusCode, Trace: parsed.Meta.Trace}
		if errs := append(parsed.Error, parsed.Errors...); len(errs) > 0 {
			apiErr.Code, apiErr.Message = errs[0].Code, errs[0].Message
		}
		return "", apiErr
	}
	if decodeErr != nil {
		return "", fmt.Errorf("kagi extract: decoding response: %w", decodeErr)
	}

	// A 200 can still fail per page: markdown is null and the reason is in
	// the page's error field or in errors.
	for _, page := range parsed.Data {
		if page.Markdown != nil && *page.Markdown != "" {
			return *page.Markdown, nil
		}
	}
	reason := "no content returned"
	switch {
	case len(parsed.Errors) > 0:
		reason = parsed.Errors[0].Code + ": " + parsed.Errors[0].Message
	case len(parsed.Data) > 0 && parsed.Data[0].Error != "":
		reason = parsed.Data[0].Error
	}
	return "", fmt.Errorf("%w: %s (trace %s)", errLinkUnreadable, reason, parsed.Meta.Trace)
}
