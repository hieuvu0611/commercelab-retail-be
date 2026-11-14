package domain

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

type fakeImageStorage struct {
	objects  map[string]ImageObject
	presigns []string
	failSign bool
}

func newFakeImageStorage() *fakeImageStorage {
	return &fakeImageStorage{objects: map[string]ImageObject{}}
}

func (f *fakeImageStorage) PresignUpload(_ context.Context, key, contentType string, size int64, ttl time.Duration) (UploadTarget, error) {
	if f.failSign {
		return UploadTarget{}, errors.New("no credentials")
	}
	f.presigns = append(f.presigns, fmt.Sprintf("%s|%s|%d", key, contentType, size))
	return UploadTarget{URL: "https://storage.test/" + key, Method: "PUT", Headers: map[string]string{"Content-Type": contentType}, ExpiresAt: priceNow.Add(ttl)}, nil
}

func (f *fakeImageStorage) Stat(_ context.Context, key string) (ImageObject, error) {
	o, ok := f.objects[key]
	if !ok {
		return ImageObject{}, ErrImageObjectMissing
	}
	return o, nil
}

func (f *fakeImageStorage) PresignDownload(_ context.Context, key string, _ time.Duration) (string, error) {
	return "https://storage.test/get/" + key, nil
}

func newImageEnv(t *testing.T) (*ImageService, *fakeImageStorage, *productEnv) {
	t.Helper()
	e := newProductEnv(t)
	storage := newFakeImageStorage()
	svc := NewImageService(e.svc, storage)
	n := 0
	svc.newID = func() string { n++; return fmt.Sprintf("%016x", n) }
	return svc, storage, e
}

func TestRequestUploadSignsAProductScopedKey(t *testing.T) {
	svc, storage, e := newImageEnv(t)
	v := e.newProduct(t, "city-backpack")
	target, err := svc.RequestUpload(context.Background(), v.ID, "image/jpeg", 1_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if target.Key != "products/"+v.ID+"/0000000000000001.jpg" || target.ImageID != "0000000000000001" || target.Method != "PUT" {
		t.Fatalf("target = %+v", target)
	}
	if got := storage.presigns[0]; got != target.Key+"|image/jpeg|1000000" {
		t.Fatalf("presigned %q", got)
	}
}

func TestRequestUploadValidation(t *testing.T) {
	svc, _, e := newImageEnv(t)
	v := e.newProduct(t, "city-backpack")
	tests := []struct {
		name, contentType string
		size              int64
		field             string
	}{
		{"gif is not allowed", "image/gif", 100, "contentType"},
		{"svg is not allowed", "image/svg+xml", 100, "contentType"},
		{"empty type", "", 100, "contentType"},
		{"text", "text/html", 100, "contentType"},
		{"zero size", "image/png", 0, "sizeBytes"},
		{"negative size", "image/png", -1, "sizeBytes"},
		{"above 5 MB", "image/png", MaxImageBytes + 1, "sizeBytes"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.RequestUpload(context.Background(), v.ID, tc.contentType, tc.size)
			if ve, ok := err.(*ValidationError); !ok || len(ve.Fields[tc.field]) == 0 {
				t.Fatalf("err = %v", err)
			}
		})
	}
	for _, ct := range []string{"image/jpeg", "image/png", "image/webp"} {
		if _, err := svc.RequestUpload(context.Background(), v.ID, ct, MaxImageBytes); err != nil {
			t.Errorf("%s at exactly 5 MB: %v", ct, err)
		}
	}
	if _, err := svc.RequestUpload(context.Background(), "nope", "image/png", 10); !errors.Is(err, ErrProductNotFound) {
		t.Fatalf("unknown product: %v", err)
	}
}

func TestRequestUploadFailsCleanlyWithoutStorage(t *testing.T) {
	e := newProductEnv(t)
	v := e.newProduct(t, "city-backpack")
	if _, err := NewImageService(e.svc, nil).RequestUpload(context.Background(), v.ID, "image/png", 10); !errors.Is(err, ErrImageStorageUnavailable) {
		t.Fatalf("err = %v", err)
	}
	svc, storage, env2 := newImageEnv(t)
	storage.failSign = true
	v = env2.newProduct(t, "city-backpack")
	if _, err := svc.RequestUpload(context.Background(), v.ID, "image/png", 10); !errors.Is(err, ErrImageStorageUnavailable) {
		t.Fatalf("signing failure: %v", err)
	}
}

func (e *productEnv) upload(storage *fakeImageStorage, productID, id, ext, contentType string, size int64) string {
	key := fmt.Sprintf("products/%s/%s.%s", productID, id, ext)
	storage.objects[key] = ImageObject{Size: size, ContentType: contentType}
	return key
}

func TestAttachAppendsAVerifiedUpload(t *testing.T) {
	svc, storage, e := newImageEnv(t)
	ctx := context.Background()
	v := e.newProduct(t, "city-backpack")
	key := e.upload(storage, v.ID, "aaaaaaaaaaaaaaaa", "png", "image/png", 2048)

	got, err := svc.Attach(ctx, "staff", v.ID, v.Version, key, " Front view ")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Images) != 1 || got.Images[0] != (ProductImage{ID: "aaaaaaaaaaaaaaaa", Key: key, Alt: "Front view", ContentType: "image/png", SizeBytes: 2048}) {
		t.Fatalf("images = %+v", got.Images)
	}
	if got.Version != v.Version+1 {
		t.Fatalf("version = %d", got.Version)
	}
	// Attaching the same file twice is rejected.
	if _, err := svc.Attach(ctx, "staff", v.ID, got.Version, key, ""); err == nil {
		t.Fatal("duplicate attach accepted")
	}
}

func TestAttachRules(t *testing.T) {
	svc, storage, e := newImageEnv(t)
	ctx := context.Background()
	v := e.newProduct(t, "city-backpack")
	other := e.newProduct(t, "other")
	good := e.upload(storage, v.ID, "bbbbbbbbbbbbbbbb", "jpg", "image/jpeg", 100)
	tests := []struct {
		name  string
		key   string
		field string
	}{
		{"missing object", "products/" + v.ID + "/cccccccccccccccc.png", "key"},
		{"another product's key", e.upload(storage, other.ID, "dddddddddddddddd", "png", "image/png", 100), "key"},
		{"key outside products", "uploads/x.png", "key"},
		{"path traversal", "products/" + v.ID + "/../" + other.ID + "/dddddddddddddddd.png", "key"},
		{"unexpected extension", "products/" + v.ID + "/eeeeeeeeeeeeeeee.gif", "key"},
		{"empty", "", "key"},
		{"too large object", e.upload(storage, v.ID, "ffffffffffffffff", "png", "image/png", MaxImageBytes+1), "key"},
		{"wrong stored type", e.upload(storage, v.ID, "1111111111111111", "png", "text/html", 100), "key"},
		{"extension and stored type disagree", e.upload(storage, v.ID, "2222222222222222", "png", "image/jpeg", 100), "key"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.Attach(ctx, "s", v.ID, v.Version, tc.key, "")
			if ve, ok := err.(*ValidationError); !ok || len(ve.Fields[tc.field]) == 0 {
				t.Fatalf("err = %v", err)
			}
		})
	}
	if _, err := svc.Attach(ctx, "s", v.ID, v.Version, good, strings.Repeat("a", 201)); err == nil {
		t.Fatal("alt text above 200 characters accepted")
	}
	if _, err := svc.Attach(ctx, "s", v.ID, 99, good, ""); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale version: %v", err)
	}
}

func TestAttachStopsAtTheImageLimit(t *testing.T) {
	svc, storage, e := newImageEnv(t)
	ctx := context.Background()
	v := e.newProduct(t, "city-backpack")
	version := v.Version
	for i := 0; i < MaxProductImages; i++ {
		key := e.upload(storage, v.ID, fmt.Sprintf("%016x", 100+i), "png", "image/png", 10)
		got, err := svc.Attach(ctx, "s", v.ID, version, key, "")
		if err != nil {
			t.Fatalf("image %d: %v", i, err)
		}
		version = got.Version
	}
	key := e.upload(storage, v.ID, "9999999999999999", "png", "image/png", 10)
	if _, err := svc.Attach(ctx, "s", v.ID, version, key, ""); err == nil {
		t.Fatal("11th image accepted")
	}
}

func TestReorderAndRemove(t *testing.T) {
	svc, storage, e := newImageEnv(t)
	ctx := context.Background()
	v := e.newProduct(t, "city-backpack")
	version := v.Version
	for _, id := range []string{"aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb", "cccccccccccccccc"} {
		got, err := svc.Attach(ctx, "s", v.ID, version, e.upload(storage, v.ID, id, "png", "image/png", 10), "")
		if err != nil {
			t.Fatal(err)
		}
		version = got.Version
	}

	got, err := svc.Reorder(ctx, "s", v.ID, version, []string{"cccccccccccccccc", "aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb"})
	if err != nil || got.Images[0].ID != "cccccccccccccccc" || got.Images[2].ID != "bbbbbbbbbbbbbbbb" {
		t.Fatalf("reorder: %+v, %v", got.Images, err)
	}
	version = got.Version
	for name, ids := range map[string][]string{
		"missing one": {"cccccccccccccccc", "aaaaaaaaaaaaaaaa"},
		"unknown id":  {"cccccccccccccccc", "aaaaaaaaaaaaaaaa", "zzzz"},
		"duplicate":   {"cccccccccccccccc", "cccccccccccccccc", "aaaaaaaaaaaaaaaa"},
		"extra":       {"cccccccccccccccc", "aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb", "aaaaaaaaaaaaaaaa"},
	} {
		if _, err := svc.Reorder(ctx, "s", v.ID, version, ids); err == nil {
			t.Errorf("%s: invalid order accepted", name)
		}
	}

	got, err = svc.Remove(ctx, "s", v.ID, version, "aaaaaaaaaaaaaaaa")
	if err != nil || len(got.Images) != 2 || got.Images[0].ID != "cccccccccccccccc" || got.Images[1].ID != "bbbbbbbbbbbbbbbb" {
		t.Fatalf("remove: %+v, %v", got.Images, err)
	}
	if _, err := svc.Remove(ctx, "s", v.ID, got.Version, "aaaaaaaaaaaaaaaa"); !errors.Is(err, ErrImageNotFound) {
		t.Fatalf("remove twice: %v", err)
	}
}

func TestImageChangesOnAPublishedProductGoThroughADraftRevision(t *testing.T) {
	svc, storage, e := newImageEnv(t)
	ctx := context.Background()
	v := e.newProduct(t, "city-backpack")
	e.publishDirectly(t, v.ID)
	live, _ := e.store.GetProduct(ctx, v.ID)
	key := e.upload(storage, v.ID, "aaaaaaaaaaaaaaaa", "png", "image/png", 10)
	got, err := svc.Attach(ctx, "s", v.ID, live.Version, key, "")
	if err != nil || !got.HasDraft || len(got.Images) != 1 {
		t.Fatalf("attach: %+v, %v", got.Product, err)
	}
	if stillLive, _ := e.store.GetProduct(ctx, v.ID); len(stillLive.Images) != 0 {
		t.Fatal("the live product must not change before publishing")
	}
}

func TestImageURLsAreFilledWithoutFailingOnSigningProblems(t *testing.T) {
	svc, _, _ := newImageEnv(t)
	got := svc.WithURLs(context.Background(), []ProductImage{{ID: "a", Key: "products/p/a.png"}})
	if got[0].URL != "https://storage.test/get/products/p/a.png" {
		t.Fatalf("url = %q", got[0].URL)
	}
	if NewImageService(nil, nil).WithURLs(context.Background(), []ProductImage{{ID: "a"}})[0].URL != "" {
		t.Fatal("without storage no url can be produced")
	}
}
