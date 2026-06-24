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

type Uploader interface {
	Upload(ctx context.Context, key string, body io.Reader, contentType string) error
	Presign(ctx context.Context, key string, expiry time.Duration) (url string, err error)

	Ping(ctx context.Context) (time.Duration, error)
}

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
			o.UsePathStyle = true
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

func (u *s3Uploader) Ping(ctx context.Context) (time.Duration, error) {
	const healthKey = "healthcheck/ping"
	start := time.Now()
	_, err := u.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(u.bucket),
		Key:    aws.String(healthKey),
	})
	latency := time.Since(start)
	if err != nil {

		var apiErr *awshttp.ResponseError
		if errors.As(err, &apiErr) && apiErr.Response.StatusCode == 404 {
			return latency, nil
		}
		return latency, err
	}
	return latency, nil
}
