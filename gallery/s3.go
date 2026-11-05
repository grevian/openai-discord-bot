package gallery

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// S3Store is an ObjectStore over the images bucket.
type S3Store struct {
	client *s3.Client
	bucket string
}

var _ ObjectStore = (*S3Store)(nil)

// NewS3Store returns a store over bucket.
func NewS3Store(cfg aws.Config, bucket string) *S3Store {
	return &S3Store{client: s3.NewFromConfig(cfg), bucket: bucket}
}

// Get implements ObjectStore.
func (s *S3Store) Get(ctx context.Context, key string) ([]byte, string, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: &key})
	if err != nil {
		var nsk *types.NoSuchKey
		if errors.As(err, &nsk) {
			return nil, "", ErrNotFound
		}
		return nil, "", fmt.Errorf("get %s: %w", key, err)
	}
	defer func() { _ = out.Body.Close() }()
	body, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, "", fmt.Errorf("read %s: %w", key, err)
	}
	return body, aws.ToString(out.ETag), nil
}

// Put implements ObjectStore. S3 reports a failed If-Match / If-None-Match as
// 412 PreconditionFailed, and two simultaneous conditional writes as 409
// ConditionalRequestConflict; both mean "someone else wrote first".
func (s *S3Store) Put(ctx context.Context, key string, body []byte, opts PutOptions) error {
	in := &s3.PutObjectInput{
		Bucket:        &s.bucket,
		Key:           &key,
		Body:          bytes.NewReader(body),
		ContentLength: aws.Int64(int64(len(body))),
	}
	if opts.ContentType != "" {
		in.ContentType = &opts.ContentType
	}
	if opts.CacheControl != "" {
		in.CacheControl = &opts.CacheControl
	}
	if opts.IfMatch != "" {
		in.IfMatch = &opts.IfMatch
	}
	if opts.IfNoneMatch {
		in.IfNoneMatch = aws.String("*")
	}
	if _, err := s.client.PutObject(ctx, in); err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) {
			switch apiErr.ErrorCode() {
			case "PreconditionFailed", "ConditionalRequestConflict":
				return fmt.Errorf("put %s: %w", key, ErrConflict)
			}
		}
		return fmt.Errorf("put %s: %w", key, err)
	}
	return nil
}
