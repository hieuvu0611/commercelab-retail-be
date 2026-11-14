//go:build integration

package s3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"
)

func integrationClient(t *testing.T) *Client {
	t.Helper()
	if os.Getenv(EndpointEnvVar) == "" {
		t.Skip("S3_ENDPOINT is not set; run `make test-integration`")
	}
	c := NewFromEnv()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := c.EnsureBucket(ctx); err != nil {
		t.Fatalf("ensure bucket: %v", err)
	}
	if err := c.EnsureBucket(ctx); err != nil {
		t.Fatalf("ensure bucket twice: %v", err)
	}
	return c
}

func send(t *testing.T, req PresignedRequest, body []byte, contentType string) *http.Response {
	t.Helper()
	httpReq, err := http.NewRequest(req.Method, req.URL, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}
	if contentType != "" {
		httpReq.Header.Set("Content-Type", contentType)
	}
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestPresignedUploadHeadAndDownloadAgainstRealStorage(t *testing.T) {
	c := integrationClient(t)
	ctx := context.Background()
	key := fmt.Sprintf("it/%d.png", time.Now().UnixNano())
	data := []byte("not-really-a-png-but-bytes")

	if _, err := c.Head(ctx, key); !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("head before upload: %v", err)
	}

	put, err := c.PresignPut(ctx, key, "image/png", int64(len(data)), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// The signature binds content type and length: a different type or size is refused.
	if resp := send(t, put, data, "image/jpeg"); resp.StatusCode < 400 {
		t.Fatalf("upload with another content type was accepted: %d", resp.StatusCode)
	}
	if resp := send(t, put, append(data, '!'), ""); resp.StatusCode < 400 {
		t.Fatalf("upload with another length was accepted: %d", resp.StatusCode)
	}
	if resp := send(t, put, data, ""); resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("signed upload failed: %d %s", resp.StatusCode, body)
	}

	info, err := c.Head(ctx, key)
	if err != nil || info.Size != int64(len(data)) || info.ContentType != "image/png" {
		t.Fatalf("head = %+v, %v", info, err)
	}

	get, err := c.PresignGet(ctx, key, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	resp := send(t, get, nil, "")
	got, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !bytes.Equal(got, data) {
		t.Fatalf("signed download: %d %q", resp.StatusCode, got)
	}

	// An unsigned request for the same private object is refused.
	anon, err := http.Get(c.ObjectURL(key))
	if err != nil {
		t.Fatal(err)
	}
	defer anon.Body.Close()
	if anon.StatusCode < 400 {
		t.Fatalf("anonymous read of a private object returned %d", anon.StatusCode)
	}
}
