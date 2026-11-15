//go:build integration

package snapshot

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/db"
)

func TestMongoStoreContract(t *testing.T) {
	uri, name := os.Getenv("MONGO_URI"), os.Getenv("MONGO_DATABASE")
	if uri == "" {
		t.Skip("MONGO_URI is not set; run `make test-integration`")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	mongoStore, _, err := db.ConnectMongo(ctx, uri, name)
	if err != nil {
		t.Fatalf("connect mongo: %v", err)
	}
	t.Cleanup(func() { _ = mongoStore.Close(context.Background()) })
	store, err := NewMongoStore(ctx, mongoStore)
	if err != nil {
		t.Fatal(err)
	}
	// NewMongoStore twice must not conflict with the index that exists already.
	if _, err := NewMongoStore(ctx, mongoStore); err != nil {
		t.Fatalf("second NewMongoStore: %v", err)
	}
	run := time.Now().UnixNano()
	var ids []string
	t.Cleanup(func() {
		for _, id := range ids {
			_, _ = mongoStore.Database().Collection(Collection).DeleteOne(context.Background(), map[string]any{"order_id": id})
		}
	})
	runContract(t, store, func() string {
		id := fmt.Sprintf("O-it-%d-%d", run, len(ids))
		ids = append(ids, id)
		return id
	})
}
