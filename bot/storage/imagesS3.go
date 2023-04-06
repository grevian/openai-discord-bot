package storage

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/segmentio/ksuid"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.uber.org/zap"
)

type ImageStorage struct {
	client     *s3.Client
	httpClient *http.Client
	bucketName string
	logger     *zap.Logger
}

func NewImageStorage(config aws.Config, logger *zap.Logger, bucketName string) *ImageStorage {
	return &ImageStorage{
		client: s3.NewFromConfig(config),
		httpClient: &http.Client{
			Transport: otelhttp.NewTransport(http.DefaultTransport),
		},
		bucketName: bucketName,
		logger:     logger,
	}
}

func (i *ImageStorage) StoreImageFromURL(ctx context.Context, groupId string, URL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, URL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create image request: %w", err)
	}

	resp, err := i.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to request image from URL: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected response status: %d", resp.StatusCode)
	}

	defer func() {
		err = resp.Body.Close()
		if err != nil {
			i.logger.Warn("failed to close image response body", zap.Error(err))
		}
	}()

	return i.StoreImage(ctx, groupId, resp.Body, resp.ContentLength)
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
		ContentLength: contentLength,
		Body:          reader,
		ContentType:   aws.String("image/png"),
	})

	if err != nil {
		return "", fmt.Errorf("failed to write image to S3: %w", err)
	}

	return *constructedKey, nil
}
