// Package storage adapts the S3 client to the catalog's image storage port.
package storage

import (
	"context"
	"errors"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/s3"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/domain"
)

// S3Images implements domain.ImageStorage on an S3-compatible bucket.
type S3Images struct{ client *s3.Client }

func NewS3Images(client *s3.Client) *S3Images { return &S3Images{client: client} }

func (s *S3Images) PresignUpload(ctx context.Context, key, contentType string, size int64, ttl time.Duration) (domain.UploadTarget, error) {
	req, err := s.client.PresignPut(ctx, key, contentType, size, ttl)
	if err != nil {
		return domain.UploadTarget{}, err
	}
	return domain.UploadTarget{URL: req.URL, Method: req.Method, Headers: req.Headers, ExpiresAt: req.ExpiresAt}, nil
}

func (s *S3Images) Stat(ctx context.Context, key string) (domain.ImageObject, error) {
	info, err := s.client.Head(ctx, key)
	if errors.Is(err, s3.ErrObjectNotFound) {
		return domain.ImageObject{}, domain.ErrImageObjectMissing
	}
	if err != nil {
		return domain.ImageObject{}, err
	}
	return domain.ImageObject{Size: info.Size, ContentType: info.ContentType}, nil
}

func (s *S3Images) PresignDownload(ctx context.Context, key string, ttl time.Duration) (string, error) {
	req, err := s.client.PresignGet(ctx, key, ttl)
	return req.URL, err
}

var _ domain.ImageStorage = (*S3Images)(nil)
