// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

// Package s3 provides S3 backing storage for newsletter images.
package s3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
)

// Client implements port.ImageStore by persisting images to S3.
type Client struct {
	s3Client           *s3.Client
	bucket             string
	cdnURLPrefix       string
	createMissingBucket bool
}

// Config holds the parameters needed to construct a Client.
type Config struct {
	Bucket              string // required
	Region              string // required
	Endpoint            string // optional; empty means real AWS, otherwise MinIO/LocalStack
	CDNURLPrefix        string // optional; empty means use service download routes
	CreateMissingBucket bool   // optional; idempotent bucket creation on startup (dev/test only)
}

// New constructs a Client with the given configuration. If createMissingBucket
// is true, it also ensures the S3 bucket exists via HeadBucket and CreateBucket.
func New(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.Bucket == "" || cfg.Region == "" {
		return nil, fmt.Errorf("S3 bucket and region are required")
	}

	// Load AWS configuration
	opts := []func(*config.LoadOptions) error{}

	// Set custom endpoint if provided (MinIO/LocalStack)
	if cfg.Endpoint != "" {
		opts = append(opts, config.WithEndpointResolver(aws.EndpointResolverFunc(
			func(service, region string) (aws.Endpoint, error) {
				if service == s3.ServiceID {
					return aws.Endpoint{
						URL:           cfg.Endpoint,
						SigningRegion: cfg.Region,
					}, nil
				}
				return aws.Endpoint{}, &smithy.GenericAPIError{Code: "UnknownService"}
			},
		)))
	}

	awsConfig, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}

	s3Client := s3.NewFromConfig(awsConfig)

	client := &Client{
		s3Client:           s3Client,
		bucket:             cfg.Bucket,
		cdnURLPrefix:       cfg.CDNURLPrefix,
		createMissingBucket: cfg.CreateMissingBucket,
	}

	// Ensure bucket exists if requested
	if cfg.CreateMissingBucket {
		if err := client.ensureBucket(ctx); err != nil {
			return nil, err
		}
	}

	return client, nil
}

// ensureBucket idempotently ensures the bucket exists. Logs at info level.
func (c *Client) ensureBucket(ctx context.Context) error {
	// Try to get bucket metadata
	_, err := c.s3Client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(c.bucket),
	})
	if err == nil {
		slog.InfoContext(ctx, "S3 bucket already exists", "bucket", c.bucket)
		return nil
	}

	// Bucket doesn't exist; try to create it
	if _, err := c.s3Client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String(c.bucket),
	}); err != nil {
		// If it failed for a reason other than bucket already exists, return the error
		var bae *types.BucketAlreadyExists
		var bao *types.BucketAlreadyOwnedByYou
		if !errors.As(err, &bae) && !errors.As(err, &bao) {
			return fmt.Errorf("create S3 bucket: %w", err)
		}
	}

	slog.InfoContext(ctx, "S3 bucket created or already exists", "bucket", c.bucket)
	return nil
}

// Put stores the image data at the given key (content hash).
// Implements port.ImageStore.
func (c *Client) Put(ctx context.Context, key string, data []byte, contentType string) error {
	uploader := manager.NewUploader(c.s3Client)
	_, err := uploader.Upload(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(c.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(data),
		ContentType: aws.String(contentType),
		// Content-addressed storage: cache forever since content never changes.
		CacheControl: aws.String("public, max-age=31536000, immutable"),
	})
	if err != nil {
		return fmt.Errorf("upload to S3: %w", err)
	}
	return nil
}

// Get retrieves the image data and content type from the given key.
// Returns domain.ErrNotFound if the key does not exist.
// Implements port.ImageStore.
func (c *Client) Get(ctx context.Context, key string) ([]byte, string, error) {
	result, err := c.s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		var nsk *types.NoSuchKey
		if errors.As(err, &nsk) {
			return nil, "", domain.ErrNotFound
		}
		return nil, "", fmt.Errorf("get from S3: %w", err)
	}
	defer result.Body.Close()

	// Read the body
	data, err := io.ReadAll(result.Body)
	if err != nil {
		return nil, "", fmt.Errorf("read S3 body: %w", err)
	}

	contentType := ""
	if result.ContentType != nil {
		contentType = *result.ContentType
	}

	return data, contentType, nil
}

// PublicURL returns the public URL for the given key.
// If CDN_URL_PREFIX is set, returns that; otherwise returns an empty string
// (the caller falls back to the service's own download route).
// Implements port.ImageStore.
func (c *Client) PublicURL(key string) string {
	if c.cdnURLPrefix == "" {
		return ""
	}
	return path.Join(c.cdnURLPrefix, key)
}
