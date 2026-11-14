package s3

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"
)

func testClient(endpoint string) *Client {
	return New(Config{
		Endpoint: endpoint, Region: "ap-southeast-1", Bucket: "assets", AccessKey: "AKIATESTKEY", SecretKey: "test-secret-key",
	})
}

func TestPresignPutIsSigV4WithContentTypeAndLengthSigned(t *testing.T) {
	req, err := testClient("").PresignPut(context.Background(), "products/p1/a.jpg", "image/jpeg", 12345, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(req.URL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if u.Host != "assets.s3.ap-southeast-1.amazonaws.com" || u.Path != "/products/p1/a.jpg" {
		t.Fatalf("url = %s", req.URL)
	}
	if q.Get("X-Amz-Algorithm") != "AWS4-HMAC-SHA256" || q.Get("X-Amz-Signature") == "" || q.Get("X-Amz-Expires") != "300" {
		t.Fatalf("not a SigV4 presigned url: %s", req.URL)
	}
	if !strings.HasPrefix(q.Get("X-Amz-Credential"), "AKIATESTKEY/") || !strings.Contains(q.Get("X-Amz-Credential"), "/ap-southeast-1/s3/aws4_request") {
		t.Fatalf("credential scope = %s", q.Get("X-Amz-Credential"))
	}
	signed := q.Get("X-Amz-SignedHeaders")
	for _, h := range []string{"content-type", "content-length", "host"} {
		if !strings.Contains(signed, h) {
			t.Fatalf("header %s is not signed: %s", h, signed)
		}
	}
	if req.Method != "PUT" || req.Headers["Content-Type"] != "image/jpeg" {
		t.Fatalf("req = %+v", req)
	}
	if strings.Contains(req.URL, "test-secret-key") {
		t.Fatal("the secret key must never appear in the url")
	}
}

func TestPresignedSignatureDependsOnTheSignedValues(t *testing.T) {
	c := testClient("")
	sig := func(key, contentType string, length int64) string {
		t.Helper()
		req, err := c.PresignPut(context.Background(), key, contentType, length, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		u, _ := url.Parse(req.URL)
		return u.Query().Get("X-Amz-Signature")
	}
	base := sig("k", "image/png", 10)
	for name, other := range map[string]string{
		"key": sig("k2", "image/png", 10), "content type": sig("k", "image/jpeg", 10), "length": sig("k", "image/png", 11),
	} {
		if other == base {
			t.Errorf("changing the %s must change the signature", name)
		}
	}
}

func TestCustomEndpointUsesPathStyle(t *testing.T) {
	c := testClient("http://localhost:9000/")
	put, err := c.PresignPut(context.Background(), "products/p1/a.png", "image/png", 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(put.URL, "http://localhost:9000/assets/products/p1/a.png?") {
		t.Fatalf("put url = %s", put.URL)
	}
	get, err := c.PresignGet(context.Background(), "products/p1/a.png", time.Hour)
	if err != nil || !strings.HasPrefix(get.URL, "http://localhost:9000/assets/products/p1/a.png?") || !strings.Contains(get.URL, "X-Amz-Expires=3600") || get.Method != "GET" {
		t.Fatalf("get = %+v, %v", get, err)
	}
	if got := c.ObjectURL("a/b.png"); got != "http://localhost:9000/assets/a/b.png" {
		t.Fatalf("object url = %s", got)
	}
	if got := testClient("").ObjectURL("a/b.png"); got != "https://assets.s3.ap-southeast-1.amazonaws.com/a/b.png" {
		t.Fatalf("aws object url = %s", got)
	}
}

func TestPresignRejectsBadInput(t *testing.T) {
	c := testClient("")
	ctx := context.Background()
	tests := map[string]func() error{
		"empty key":        func() error { _, err := c.PresignPut(ctx, "", "image/png", 10, time.Minute); return err },
		"empty type":       func() error { _, err := c.PresignPut(ctx, "k", "", 10, time.Minute); return err },
		"zero length":      func() error { _, err := c.PresignPut(ctx, "k", "image/png", 0, time.Minute); return err },
		"zero ttl":         func() error { _, err := c.PresignPut(ctx, "k", "image/png", 10, 0); return err },
		"ttl above 7 days": func() error { _, err := c.PresignGet(ctx, "k", 8*24*time.Hour); return err },
		"empty get key":    func() error { _, err := c.PresignGet(ctx, "", time.Minute); return err },
	}
	for name, do := range tests {
		if do() == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestSigningFailsWithoutCredentials(t *testing.T) {
	c := New(Config{Bucket: "assets"})
	if _, err := c.PresignPut(context.Background(), "k", "image/png", 10, time.Minute); err == nil {
		t.Fatal("signing without credentials must fail")
	}
}

func TestNewFromEnvDefaults(t *testing.T) {
	t.Setenv(EndpointEnvVar, "")
	t.Setenv(BucketEnvVar, "")
	t.Setenv(RegionEnvVar, "")
	c := NewFromEnv()
	if c.Bucket != "commerce-lab-retail-assets" || c.Region != "ap-southeast-1" {
		t.Fatalf("defaults = %s / %s", c.Bucket, c.Region)
	}
}

func TestPublicEndpointIsUsedInSignedURLsOnly(t *testing.T) {
	c := New(Config{
		Endpoint: "http://minio:9000", PublicEndpoint: "http://localhost:9000", Bucket: "assets", AccessKey: "AKIATESTKEY", SecretKey: "s",
	})
	req, err := c.PresignPut(context.Background(), "k.png", "image/png", 10, time.Minute)
	if err != nil || !strings.HasPrefix(req.URL, "http://localhost:9000/assets/k.png?") {
		t.Fatalf("put = %+v, %v", req, err)
	}
	if got := c.ObjectURL("k.png"); got != "http://localhost:9000/assets/k.png" {
		t.Fatalf("object url = %s", got)
	}
	if c.endpoint != "http://minio:9000" {
		t.Fatalf("internal endpoint = %s", c.endpoint)
	}
}
