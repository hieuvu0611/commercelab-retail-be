package db

import (
	"context"
	"testing"
)

func TestConnectorsFailFastOnInvalidEndpoint(t *testing.T) {
	ctx := context.Background()

	if _, err := ConnectPostgres(ctx, "postgres://invalid:invalid@127.0.0.1:1/commerce_lab?sslmode=disable"); err == nil {
		t.Fatalf("expected error connecting to unreachable postgres port")
	}
	if _, _, err := ConnectMongo(ctx, "mongodb://127.0.0.1:1", "commerce_lab_doc"); err == nil {
		t.Fatalf("expected error connecting to unreachable mongo port")
	}
	if _, err := ConnectRedis(ctx, "127.0.0.1:1", ""); err == nil {
		t.Fatalf("expected error connecting to unreachable redis port")
	}
}
