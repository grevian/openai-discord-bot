package storage

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/segmentio/ksuid"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

type ImageStorage struct {
	client     *s3.Client
	httpClient *http.Client
	bucketName string
}

func NewImageStorage(config aws.Config, bucketName string) *ImageStorage {
	return &ImageStorage{
		client:     s3.NewFromConfig(config),
		httpClient: newGuardedClient(isBlockedAddr),
		bucketName: bucketName,
	}
}

// newGuardedClient returns the HTTP client used for fetching user-linked
// images: it only speaks http/https, follows at most a few redirects, and
// refuses to connect to internal addresses (see ssrf.go).
func newGuardedClient(blocked func(netip.Addr) bool) *http.Client {
	return &http.Client{
		Transport:     otelhttp.NewTransport(newGuardedTransport(blocked)),
		CheckRedirect: checkRedirect,
	}
}

// GetImageFromURL fetches an image to edit. The URL comes from a Discord
// message attachment, so it is treated as untrusted: only http/https, and the
// client refuses internal destinations at dial time, including after redirects.
func (i *ImageStorage) GetImageFromURL(ctx context.Context, URL string) (io.ReadCloser, int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, URL, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to create image request: %w", err)
	}
	if err := checkScheme(req.URL); err != nil {
		return nil, 0, fmt.Errorf("refusing image URL: %w", err)
	}

	resp, err := i.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to request image from URL: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		// Drain a bounded amount so the connection can be reused, then close
		// the body on every path. Returning without closing leaks the connection.
		_, _ = io.CopyN(io.Discard, resp.Body, 64<<10)
		_ = resp.Body.Close()
		return nil, 0, fmt.Errorf("unexpected response status: %d", resp.StatusCode)
	}

	return resp.Body, resp.ContentLength, nil
}

func (i *ImageStorage) StoreImage(ctx context.Context, groupId string, reader io.Reader, contentLength int64) (string, error) {
	uid, err := ksuid.NewRandomWithTime(time.Now())
	if err != nil {
		return "", fmt.Errorf("somehow failed to generate a uid: %w", err)
	}

	if groupId == "" {
		groupId = "private-chat"
	}
	constructedKey := aws.String(groupId + "/" + uid.String())
	_, err = i.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(i.bucketName),
		Key:           constructedKey,
		Body:          reader,
		ContentLength: &contentLength,
		ContentType:   aws.String("image/png"),
	})

	if err != nil {
		return "", fmt.Errorf("failed to write image to S3: %w", err)
	}

	return *constructedKey, nil
}
