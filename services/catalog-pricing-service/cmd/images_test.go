package main

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth/authtest"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/domain"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/repository"
)

type memStorage struct{ objects map[string]domain.ImageObject }

func (m *memStorage) PresignUpload(_ context.Context, key, ct string, _ int64, ttl time.Duration) (domain.UploadTarget, error) {
	return domain.UploadTarget{URL: "https://storage.test/put/" + key, Method: "PUT", Headers: map[string]string{"Content-Type": ct}, ExpiresAt: time.Now().Add(ttl)}, nil
}
func (m *memStorage) Stat(_ context.Context, key string) (domain.ImageObject, error) {
	o, ok := m.objects[key]
	if !ok {
		return domain.ImageObject{}, domain.ErrImageObjectMissing
	}
	return o, nil
}
func (m *memStorage) PresignDownload(_ context.Context, key string, _ time.Duration) (string, error) {
	return "https://storage.test/get/" + key, nil
}

func newImageHTTPEnv(t *testing.T, withStorage bool) (*productEnv, *memStorage, domain.ProductView) {
	t.Helper()
	authtest.Configure(t)
	catStore := domain.NewMemoryCategoryStore()
	cats := domain.NewCategoryService(catStore)
	prods := domain.NewProductService(domain.NewMemoryProductStore(), cats)
	storage := &memStorage{objects: map[string]domain.ImageObject{}}
	var images *domain.ImageService
	if withStorage {
		images = domain.NewImageService(prods, storage)
	}
	srv := NewServerWithDeps(repository.NewCatalogPricingRepository(nil, nil), false, false, Deps{Categories: cats, Products: prods, Images: images})
	env := &productEnv{categoryEnv: &categoryEnv{srv: srv, store: catStore}}
	env.category = env.create(t, "", "City", "city")
	return env, storage, env.createProduct(t, "city-backpack")
}

func TestImageEndpointsRequireProductStaff(t *testing.T) {
	e, _, p := newImageHTTPEnv(t, true)
	base := "/api/v1/admin/products/" + p.ID + "/images"
	calls := []struct{ method, path, body string }{
		{http.MethodPost, base + "/upload-url", `{"contentType":"image/png","sizeBytes":10}`},
		{http.MethodPost, base, `{"key":"x","version":1}`},
		{http.MethodPut, base + "/order", `{"imageIds":[],"version":1}`},
		{http.MethodDelete, base + "/abc", ""},
	}
	for _, c := range calls {
		if rec := e.do(c.method, c.path, "", c.body); rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s no token: %d", c.path, rec.Code)
		}
		if rec := e.do(c.method, c.path, token(t, "MARKETING"), c.body); rec.Code != http.StatusForbidden {
			t.Fatalf("%s MARKETING: %d", c.path, rec.Code)
		}
	}
}

func TestImageUploadAttachReorderRemoveOverHTTP(t *testing.T) {
	e, storage, p := newImageHTTPEnv(t, true)
	staff := token(t, "PRODUCT_STAFF")
	base := "/api/v1/admin/products/" + p.ID + "/images"

	if rec := e.do(http.MethodPost, base+"/upload-url", staff, `{"contentType":"image/gif","sizeBytes":10}`); rec.Code != 400 {
		t.Fatalf("gif: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do(http.MethodPost, base+"/upload-url", staff, `{"contentType":"image/png","sizeBytes":5242881}`); rec.Code != 400 {
		t.Fatalf("too big: %d %s", rec.Code, rec.Body)
	}
	version := p.Version
	var ids []string
	for i := 0; i < 2; i++ {
		rec := e.do(http.MethodPost, base+"/upload-url", staff, `{"contentType":"image/png","sizeBytes":1000}`)
		var up struct {
			Data domain.UploadTarget `json:"data"`
		}
		up = decode[struct {
			Data domain.UploadTarget `json:"data"`
		}](t, rec)
		if rec.Code != 201 || up.Data.Method != "PUT" || up.Data.Headers["Content-Type"] != "image/png" || up.Data.Key == "" {
			t.Fatalf("upload-url: %d %s", rec.Code, rec.Body)
		}
		// Attaching before the upload is rejected, after it succeeds.
		if rec := e.do(http.MethodPost, base, staff, `{"key":"`+up.Data.Key+`","version":`+itoa(version)+`}`); rec.Code != 400 {
			t.Fatalf("attach before upload: %d %s", rec.Code, rec.Body)
		}
		storage.objects[up.Data.Key] = domain.ImageObject{Size: 1000, ContentType: "image/png"}
		rec = e.do(http.MethodPost, base, staff, `{"key":"`+up.Data.Key+`","alt":"Front","version":`+itoa(version)+`}`)
		got := decode[productResp](t, rec).Data
		if rec.Code != 201 || len(got.Images) != i+1 || got.Images[i].URL == "" {
			t.Fatalf("attach: %d %s", rec.Code, rec.Body)
		}
		version = got.Version
		ids = append(ids, up.Data.ImageID)
	}

	rec := e.do(http.MethodPut, base+"/order", staff, `{"imageIds":["`+ids[1]+`","`+ids[0]+`"],"version":`+itoa(version)+`}`)
	got := decode[productResp](t, rec).Data
	if rec.Code != 200 || got.Images[0].ID != ids[1] {
		t.Fatalf("reorder: %d %s", rec.Code, rec.Body)
	}
	if rec = e.do(http.MethodPut, base+"/order", staff, `{"imageIds":["`+ids[0]+`"],"version":`+itoa(got.Version)+`}`); rec.Code != 400 {
		t.Fatalf("partial order: %d", rec.Code)
	}
	rec = e.doWith(http.MethodDelete, base+"/"+ids[0], staff, "", itoa(got.Version))
	got = decode[productResp](t, rec).Data
	if rec.Code != 200 || len(got.Images) != 1 {
		t.Fatalf("remove: %d %s", rec.Code, rec.Body)
	}
	if rec = e.doWith(http.MethodDelete, base+"/"+ids[0], staff, "", itoa(got.Version)); rec.Code != 404 || decode[errorBody](t, rec).Code != "IMAGE_NOT_FOUND" {
		t.Fatalf("remove twice: %d %s", rec.Code, rec.Body)
	}
	if rec = e.doWith(http.MethodDelete, base+"/"+ids[1], staff, "", "1"); rec.Code != 409 {
		t.Fatalf("stale version: %d", rec.Code)
	}
}

func TestImageEndpointsAnswer503WithoutStorage(t *testing.T) {
	e, _, p := newImageHTTPEnv(t, false)
	rec := e.do(http.MethodPost, "/api/v1/admin/products/"+p.ID+"/images/upload-url", token(t, "PRODUCT_STAFF"), `{"contentType":"image/png","sizeBytes":10}`)
	if rec.Code != 503 || decode[errorBody](t, rec).Code != "IMAGE_STORAGE_UNAVAILABLE" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}
