package domain

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

const (
	// MaxImageBytes is the largest accepted product image (5 MB).
	MaxImageBytes int64 = 5 << 20
	// MaxProductImages is the most images one product can have.
	MaxProductImages = 10

	maxImageAltLen = 200
	uploadURLTTL   = 10 * time.Minute
	downloadURLTTL = time.Hour
)

var (
	ErrImageNotFound           = errors.New("image not found")
	ErrImageObjectMissing      = errors.New("the uploaded file was not found in storage")
	ErrImageStorageUnavailable = errors.New("image storage is not available")
)

// allowedImageTypes maps an accepted content type to the file extension used in storage keys.
var allowedImageTypes = map[string]string{"image/jpeg": "jpg", "image/png": "png", "image/webp": "webp"}

// UploadTarget tells a client where and how to upload one image directly to storage.
type UploadTarget struct {
	URL       string            `json:"uploadUrl"`
	Method    string            `json:"method"`
	Headers   map[string]string `json:"headers"`
	Key       string            `json:"key"`
	ImageID   string            `json:"imageId"`
	ExpiresAt time.Time         `json:"expiresAt"`
}

// ImageObject describes a file found in storage.
type ImageObject struct {
	Size        int64
	ContentType string
}

// ImageStorage is the object storage the image workflow needs. Stat returns ErrImageObjectMissing for an
// unknown key.
type ImageStorage interface {
	PresignUpload(ctx context.Context, key, contentType string, size int64, ttl time.Duration) (UploadTarget, error)
	Stat(ctx context.Context, key string) (ImageObject, error)
	PresignDownload(ctx context.Context, key string, ttl time.Duration) (string, error)
}

// ImageService manages product images: signed uploads, attaching, ordering and removing. Changes follow the
// product edit rules (versions, draft revisions for published products).
type ImageService struct {
	products *ProductService
	storage  ImageStorage // nil when no storage is configured
	newID    func() string
}

func NewImageService(products *ProductService, storage ImageStorage) *ImageService {
	return &ImageService{products: products, storage: storage, newID: newImageID}
}

func newImageID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return hex.EncodeToString(b)
}

func invalid(field, msg string) error {
	return &ValidationError{Fields: map[string][]string{field: {msg}}}
}

// RequestUpload returns a signed upload for one image of the product. The signature fixes the content type and
// the exact size, so the storage server rejects anything else.
func (s *ImageService) RequestUpload(ctx context.Context, productID, contentType string, sizeBytes int64) (UploadTarget, error) {
	ext, ok := allowedImageTypes[contentType]
	fields := map[string][]string{}
	if !ok {
		fields["contentType"] = []string{"content type must be image/jpeg, image/png or image/webp"}
	}
	if sizeBytes < 1 || sizeBytes > MaxImageBytes {
		fields["sizeBytes"] = []string{fmt.Sprintf("size must be between 1 and %d bytes", MaxImageBytes)}
	}
	if len(fields) > 0 {
		return UploadTarget{}, &ValidationError{Fields: fields}
	}
	if s.storage == nil {
		return UploadTarget{}, ErrImageStorageUnavailable
	}
	if _, err := s.products.store.GetProduct(ctx, productID); err != nil {
		return UploadTarget{}, err
	}
	imageID := s.newID()
	key := fmt.Sprintf("products/%s/%s.%s", productID, imageID, ext)
	target, err := s.storage.PresignUpload(ctx, key, contentType, sizeBytes, uploadURLTTL)
	if err != nil {
		return UploadTarget{}, fmt.Errorf("%w: %v", ErrImageStorageUnavailable, err)
	}
	target.Key, target.ImageID = key, imageID
	return target, nil
}

func imageKeyPattern(productID string) *regexp.Regexp {
	return regexp.MustCompile(`^products/` + regexp.QuoteMeta(productID) + `/([0-9a-f]{16})\.(jpg|png|webp)$`)
}

func (s *ImageService) currentImages(ctx context.Context, productID string, expectedVersion int64) ([]ProductImage, error) {
	view, err := s.products.GetProduct(ctx, productID)
	if err != nil {
		return nil, err
	}
	if view.Version != expectedVersion {
		return nil, ErrVersionConflict
	}
	return slices.Clone(view.Images), nil
}

func (s *ImageService) apply(ctx context.Context, actor, productID string, expectedVersion int64, images []ProductImage) (ProductView, error) {
	return s.products.UpdateProduct(ctx, actor, productID, expectedVersion, ProductPatch{Images: &images})
}

// Attach adds an uploaded file to the product after checking that it exists in storage with an allowed type
// and size.
func (s *ImageService) Attach(ctx context.Context, actor, productID string, expectedVersion int64, key, alt string) (ProductView, error) {
	alt = strings.TrimSpace(alt)
	if len(alt) > maxImageAltLen {
		return ProductView{}, invalid("alt", fmt.Sprintf("alt text must be at most %d characters", maxImageAltLen))
	}
	match := imageKeyPattern(productID).FindStringSubmatch(key)
	if match == nil {
		return ProductView{}, invalid("key", "key must be one returned by the upload-url request of this product")
	}
	if s.storage == nil {
		return ProductView{}, ErrImageStorageUnavailable
	}
	images, err := s.currentImages(ctx, productID, expectedVersion)
	if err != nil {
		return ProductView{}, err
	}
	if len(images) >= MaxProductImages {
		return ProductView{}, invalid("images", fmt.Sprintf("a product can have at most %d images", MaxProductImages))
	}
	if slices.ContainsFunc(images, func(i ProductImage) bool { return i.ID == match[1] }) {
		return ProductView{}, invalid("key", "this image is already attached")
	}
	obj, err := s.storage.Stat(ctx, key)
	switch {
	case errors.Is(err, ErrImageObjectMissing):
		return ProductView{}, invalid("key", "the file has not been uploaded yet")
	case err != nil:
		return ProductView{}, fmt.Errorf("%w: %v", ErrImageStorageUnavailable, err)
	case obj.Size < 1 || obj.Size > MaxImageBytes:
		return ProductView{}, invalid("key", fmt.Sprintf("the uploaded file must be between 1 and %d bytes", MaxImageBytes))
	case allowedImageTypes[obj.ContentType] != match[2]:
		return ProductView{}, invalid("key", "the uploaded file type does not match its extension or is not allowed")
	}
	images = append(images, ProductImage{ID: match[1], Key: key, Alt: alt, ContentType: obj.ContentType, SizeBytes: obj.Size})
	return s.apply(ctx, actor, productID, expectedVersion, images)
}

// Reorder sets the display order; imageIDs must contain every current image exactly once.
func (s *ImageService) Reorder(ctx context.Context, actor, productID string, expectedVersion int64, imageIDs []string) (ProductView, error) {
	images, err := s.currentImages(ctx, productID, expectedVersion)
	if err != nil {
		return ProductView{}, err
	}
	byID := make(map[string]ProductImage, len(images))
	for _, img := range images {
		byID[img.ID] = img
	}
	ordered := make([]ProductImage, 0, len(imageIDs))
	for _, id := range imageIDs {
		img, ok := byID[id]
		if !ok {
			return ProductView{}, invalid("imageIds", "imageIds must list every current image exactly once")
		}
		delete(byID, id)
		ordered = append(ordered, img)
	}
	if len(byID) > 0 {
		return ProductView{}, invalid("imageIds", "imageIds must list every current image exactly once")
	}
	return s.apply(ctx, actor, productID, expectedVersion, ordered)
}

// Remove detaches an image from the product. The stored file is kept: a pending or earlier revision may still
// point at it.
func (s *ImageService) Remove(ctx context.Context, actor, productID string, expectedVersion int64, imageID string) (ProductView, error) {
	images, err := s.currentImages(ctx, productID, expectedVersion)
	if err != nil {
		return ProductView{}, err
	}
	rest := slices.DeleteFunc(slices.Clone(images), func(i ProductImage) bool { return i.ID == imageID })
	if len(rest) == len(images) {
		return ProductView{}, ErrImageNotFound
	}
	return s.apply(ctx, actor, productID, expectedVersion, rest)
}

// WithURLs returns a copy of images with a time-limited download URL filled in. A signing problem leaves the
// URL empty instead of failing the whole response.
func (s *ImageService) WithURLs(ctx context.Context, images []ProductImage) []ProductImage {
	out := slices.Clone(images)
	for i := range out {
		// Seed and imported images may point at an external https URL instead of a stored object.
		if strings.HasPrefix(out[i].Key, "https://") || strings.HasPrefix(out[i].Key, "http://") {
			out[i].URL = out[i].Key
		}
	}
	if s == nil || s.storage == nil {
		return out
	}
	for i := range out {
		if out[i].Key == "" || out[i].URL != "" {
			continue
		}
		if url, err := s.storage.PresignDownload(ctx, out[i].Key, downloadURLTTL); err == nil {
			out[i].URL = url
		}
	}
	return out
}
