//go:build integration

package storage

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/s3"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/domain"
)

// The full image flow against real S3-compatible storage: upload-url -> direct PUT -> attach -> signed URL.
func TestImageFlowAgainstRealStorage(t *testing.T) {
	if os.Getenv(s3.EndpointEnvVar) == "" {
		t.Skip("S3_ENDPOINT is not set; run `make test-integration`")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	client := s3.NewFromEnv()
	if err := client.EnsureBucket(ctx); err != nil {
		t.Fatal(err)
	}

	cats := domain.NewMemoryCategoryStore()
	products := domain.NewProductService(domain.NewMemoryProductStore(), cats)
	images := domain.NewImageService(products, NewS3Images(client))
	p, err := products.CreateProduct(ctx, "staff", domain.ProductInput{Slug: "it-product", Name: "IT product"})
	if err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte{0xFF}, 4096)

	target, err := images.RequestUpload(ctx, p.ID, "image/webp", int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	// Attaching before the upload fails.
	if _, err := images.Attach(ctx, "staff", p.ID, p.Version, target.Key, ""); err == nil {
		t.Fatal("attach before upload succeeded")
	}
	req, _ := http.NewRequest(target.Method, target.URL, bytes.NewReader(data))
	for k, v := range target.Headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload status %d", resp.StatusCode)
	}

	view, err := images.Attach(ctx, "staff", p.ID, p.Version, target.Key, "Real upload")
	if err != nil || len(view.Images) != 1 || view.Images[0].SizeBytes != int64(len(data)) || view.Images[0].ContentType != "image/webp" {
		t.Fatalf("attach: %+v, %v", view.Images, err)
	}
	withURL := images.WithURLs(ctx, view.Images)
	got, err := http.Get(withURL[0].URL)
	if err != nil {
		t.Fatal(err)
	}
	defer got.Body.Close()
	body, _ := io.ReadAll(got.Body)
	if got.StatusCode != http.StatusOK || !bytes.Equal(body, data) {
		t.Fatalf("download via signed url: %d (%d bytes)", got.StatusCode, len(body))
	}
}
