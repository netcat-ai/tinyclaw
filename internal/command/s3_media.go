package command

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type S3MediaStoreConfig struct {
	Endpoint        string
	Bucket          string
	Region          string
	AccessKeyID     string
	SecretAccessKey string
	ForcePathStyle  bool
	URLTTL          time.Duration
}

type S3MediaStore struct {
	client *minio.Client
	bucket string
	ttl    time.Duration
}

func NewS3MediaStore(config S3MediaStoreConfig) (*S3MediaStore, error) {
	endpoint, secure, err := parseS3Endpoint(config.Endpoint)
	if err != nil {
		return nil, err
	}
	bucket := strings.TrimSpace(config.Bucket)
	if bucket == "" {
		return nil, fmt.Errorf("generated media s3 bucket is required")
	}
	if strings.TrimSpace(config.AccessKeyID) == "" {
		return nil, fmt.Errorf("generated media s3 access key id is required")
	}
	if strings.TrimSpace(config.SecretAccessKey) == "" {
		return nil, fmt.Errorf("generated media s3 secret access key is required")
	}
	options := &minio.Options{
		Creds:  credentials.NewStaticV4(config.AccessKeyID, config.SecretAccessKey, ""),
		Secure: secure,
		Region: strings.TrimSpace(config.Region),
	}
	if config.ForcePathStyle {
		options.BucketLookup = minio.BucketLookupPath
	}
	client, err := minio.New(endpoint, options)
	if err != nil {
		return nil, fmt.Errorf("create s3 client: %w", err)
	}
	ttl := config.URLTTL
	if ttl <= 0 {
		ttl = defaultMediaURLTTL
	}
	return &S3MediaStore{client: client, bucket: bucket, ttl: ttl}, nil
}

func (s *S3MediaStore) StoreGeneratedMedia(ctx context.Context, input StoreMediaInput) (StoredMedia, error) {
	if s == nil || s.client == nil {
		return StoredMedia{}, fmt.Errorf("generated media s3 store is not configured")
	}
	mediaID := strings.TrimSpace(input.MediaID)
	if mediaID == "" {
		return StoredMedia{}, fmt.Errorf("media id is required")
	}
	if len(input.Bytes) == 0 {
		return StoredMedia{}, fmt.Errorf("media bytes are required")
	}
	mimeType := strings.TrimSpace(input.MIMEType)
	if mimeType == "" {
		mimeType = "image/png"
	}
	ttl := input.TTL
	if ttl <= 0 {
		ttl = s.ttl
	}
	if ttl <= 0 {
		ttl = defaultMediaURLTTL
	}
	objectKey := generatedMediaObjectKey(time.Now().UTC(), mediaID)
	_, err := s.client.PutObject(ctx, s.bucket, objectKey, bytes.NewReader(input.Bytes), int64(len(input.Bytes)), minio.PutObjectOptions{
		ContentType: mimeType,
	})
	if err != nil {
		return StoredMedia{}, fmt.Errorf("upload generated media: %w", err)
	}
	values := url.Values{}
	values.Set("response-content-type", mimeType)
	presigned, err := s.client.PresignedGetObject(ctx, s.bucket, objectKey, ttl, values)
	if err != nil {
		return StoredMedia{}, fmt.Errorf("presign generated media: %w", err)
	}
	return StoredMedia{
		URL:       presigned.String(),
		URLKind:   "presigned_s3",
		ExpiresAt: time.Now().UTC().Add(ttl),
	}, nil
}

func generatedMediaObjectKey(now time.Time, mediaID string) string {
	return "generated-media/" + now.UTC().Format("2006/01/02") + "/" + strings.TrimSpace(mediaID) + ".png"
}

func parseS3Endpoint(raw string) (string, bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false, fmt.Errorf("generated media s3 endpoint is required")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", false, fmt.Errorf("parse generated media s3 endpoint: %w", err)
	}
	if parsed.Scheme == "" {
		return raw, true, nil
	}
	if parsed.Host == "" {
		return "", false, fmt.Errorf("generated media s3 endpoint host is required")
	}
	switch parsed.Scheme {
	case "http":
		return parsed.Host, false, nil
	case "https":
		return parsed.Host, true, nil
	default:
		return "", false, fmt.Errorf("generated media s3 endpoint scheme must be http or https")
	}
}
