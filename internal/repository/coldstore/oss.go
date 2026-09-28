package coldstore

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss/credentials"

	"github.com/quant4dad/config"
)

// OSSStore implements ColdStore on top of Alibaba Cloud OSS.
type OSSStore struct {
	client *oss.Client
	bucket string
}

// NewOSSStore builds the OSS client from config, with static ak/sk credentials — which in
// production should be injected from environment variables.
func NewOSSStore(cfg config.OSSConfig) (*OSSStore, error) {
	if cfg.Bucket == "" {
		return nil, errors.New("coldstore: oss bucket is empty")
	}
	c := oss.LoadDefaultConfig().
		WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.AccessKeySecret))
	if cfg.Region != "" {
		c = c.WithRegion(cfg.Region)
	}
	if cfg.Endpoint != "" {
		c = c.WithEndpoint(cfg.Endpoint)
	}
	return &OSSStore{client: oss.NewClient(c), bucket: cfg.Bucket}, nil
}

func (s *OSSStore) Put(ctx context.Context, key string, r io.Reader) error {
	_, err := s.client.PutObject(ctx, &oss.PutObjectRequest{
		Bucket: oss.Ptr(s.bucket),
		Key:    oss.Ptr(key),
		Body:   r,
	})
	if err != nil {
		return fmt.Errorf("coldstore oss put %q: %w", key, err)
	}
	return nil
}

func (s *OSSStore) Exists(ctx context.Context, key string) (bool, error) {
	_, err := s.client.HeadObject(ctx, &oss.HeadObjectRequest{
		Bucket: oss.Ptr(s.bucket),
		Key:    oss.Ptr(key),
	})
	if err == nil {
		return true, nil
	}
	var se *oss.ServiceError
	if errors.As(err, &se) && se.StatusCode == 404 {
		return false, nil
	}
	return false, fmt.Errorf("coldstore oss head %q: %w", key, err)
}
