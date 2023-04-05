package storage

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type ImageStorage struct {
	client     *s3.Client
	bucketName string
}

func NewImageStorage(config aws.Config, bucketName string) *ImageStorage {
	return &ImageStorage{
		client:     nil,
		bucketName: bucketName,
	}
}

func (i *ImageStorage) StoreImage(ctx context.Context, URL string) error {
	_, err := i.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:             aws.String(i.bucketName),
		Key:                nil, // TODO Construct a key as something like, "server_id / uuid"
		Body:               nil, // TODO Instantiate an http client and read the target URL to it
		ContentDisposition: nil, // TODO Set as jpeg or whatever openai returns as (Read from http client?)
		ContentEncoding:    nil,
		ContentLength:      0,
		ContentType:        nil,
		Metadata:           nil, // TODO Not sure if we need/want these other parameters
	})

	// TODO Return our constructed key
	return err
}
