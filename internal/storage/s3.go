package storage

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	awshttp "github.com/aws/smithy-go/transport/http"
)

// Uploader abstracts object storage operations. The interface returns plain
// types so callers don't depend on the AWS SDK directly.
type Uploader interface {
	Upload(ctx context.Context, key string, body io.Reader, contentType string) error
	Presign(ctx context.Context, key string, expiry time.Duration) (url string, err error)
	// Ping does a HeadObject on a health-check key and returns the
	// round-trip duration. Used by the status command to report S3/B2
	// latency. A 404 is treated as success (the key doesn't need to
	// exist — we just want to measure the round-trip).
	Ping(ctx context.Context) (time.Duration, error)
}

// S3Config holds the credentials and endpoint for an S3-compatible bucket.
type S3Config struct {
	Endpoint  string
	Bucket    string
	Region    string
	AccessKey string
	SecretKey string
}

type s3Uploader struct {
	client    *s3.Client
	presigner *s3.PresignClient
	bucket    string
}

// NewS3Uploader creates a new S3-compatible uploader. The returned
// Uploader is backed by AWS SDK Go v2 and supports presigned GET URLs.
// Set cfg.Endpoint to a non-empty value for R2/MinIO/custom endpoints.
func NewS3Uploader(ctx context.Context, cfg S3Config) (Uploader, error) {
	awsCfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(cfg.Region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, "")),
	)
	if err != nil {
		return nil, err
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
			o.UsePathStyle = true // required for R2/MinIO
		}
	})
	return &s3Uploader{
		client:    client,
		presigner: s3.NewPresignClient(client),
		bucket:    cfg.Bucket,
	}, nil
}

func (u *s3Uploader) Upload(ctx context.Context, key string, body io.Reader, contentType string) error {
	_, err := u.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(u.bucket),
		Key:         aws.String(key),
		Body:        body,
		ContentType: aws.String(contentType),
	})
	return err
}

func (u *s3Uploader) Presign(ctx context.Context, key string, expiry time.Duration) (string, error) {
	req, err := u.presigner.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(u.bucket),
		Key:    aws.String(key),
	}, func(po *s3.PresignOptions) {
		po.Expires = expiry
	})
	if err != nil {
		return "", err
	}
	return req.URL, nil
}

// Ping measures S3/B2 round-trip latency by issuing a HeadObject on a
// health-check key. A 404 response is treated as success — we only care
// about the network round-trip, not whether the key exists.
func (u *s3Uploader) Ping(ctx context.Context) (time.Duration, error) {
	const healthKey = "healthcheck/ping"
	start := time.Now()
	_, err := u.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(u.bucket),
		Key:    aws.String(healthKey),
	})
	latency := time.Since(start)
	if err != nil {
		// A 404 is expected (the health key doesn't exist). The round-trip
		// still completed, so we report success with the measured latency.
		var apiErr *awshttp.ResponseError
		if errors.As(err, &apiErr) && apiErr.Response.StatusCode == 404 {
			return latency, nil
		}
		return latency, err
	}
	return latency, nil
}
