package coldstore

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss/credentials"
	"github.com/quant4dad/config"
)

type OSSStore struct {
	client *oss.Client
	bucket string
}

func NewOSSStore(cfg config.OSSConfig) (*OSSStore, error) {
	if cfg.Bucket == "" {
		return nil, ErrInvalidObject
	}
	c := oss.LoadDefaultConfig().WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.AccessKeySecret)).WithConnectTimeout(5 * time.Second).WithReadWriteTimeout(30 * time.Second)
	if cfg.Region != "" {
		c = c.WithRegion(cfg.Region)
	}
	if cfg.Endpoint != "" {
		c = c.WithEndpoint(cfg.Endpoint)
	}
	return &OSSStore{client: oss.NewClient(c), bucket: cfg.Bucket}, nil
}
func storeError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var remote *oss.ServiceError
	if errors.As(err, &remote) && remote.StatusCode == 404 {
		return ErrObjectNotFound
	}
	// SDK errors may carry endpoints, authorization identifiers or response text.
	return ErrStoreUnavailable
}
func (s *OSSStore) Verify(ctx context.Context, key string, digest Digest) error {
	if !validObject(key, digest) {
		return ErrInvalidObject
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	object, err := s.client.GetObject(ctx, &oss.GetObjectRequest{Bucket: oss.Ptr(s.bucket), Key: oss.Ptr(key)})
	if err != nil {
		return storeError(ctx, err)
	}
	defer object.Body.Close()
	if object.ContentLength != digest.Size {
		return ErrObjectConflict
	}
	err = verifyReader(ctx, object.Body, digest)
	if err != nil && !errors.Is(err, ErrObjectConflict) {
		return storeError(ctx, err)
	}
	return err
}
func (s *OSSStore) PutImmutable(ctx context.Context, key string, r io.ReadSeeker, digest Digest) error {
	if err := validateSource(ctx, key, r, digest); err != nil {
		return err
	}
	if err := s.Verify(ctx, key, digest); err == nil {
		return nil
	} else if !errors.Is(err, ErrObjectNotFound) {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	_, err := s.client.PutObject(ctx, &oss.PutObjectRequest{Bucket: oss.Ptr(s.bucket), Key: oss.Ptr(key), Body: r,
		ContentLength: oss.Ptr(digest.Size), ForbidOverwrite: oss.Ptr("true"), Metadata: map[string]string{"sha256": digest.SHA256}})
	if err != nil {
		var remote *oss.ServiceError
		if !errors.As(err, &remote) || remote.StatusCode != 409 {
			return storeError(ctx, err)
		}
	}
	return s.Verify(ctx, key, digest)
}

// Probe performs only a bounded bucket-information read. It confirms connection
// and read permission, not permission to upload, and never runs an archive job.
func (s *OSSStore) Probe(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err := s.client.GetBucketInfo(ctx, &oss.GetBucketInfoRequest{Bucket: oss.Ptr(s.bucket)})
	if err == nil {
		return nil
	}
	var remote *oss.ServiceError
	if errors.As(err, &remote) && (remote.StatusCode == 401 || remote.StatusCode == 403) {
		return ErrProbeRejected
	}
	return storeError(ctx, err)
}
