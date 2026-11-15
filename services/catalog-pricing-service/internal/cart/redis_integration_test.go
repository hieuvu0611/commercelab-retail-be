//go:build integration

package cart

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/db"
)

func TestRedisStoreKeepsAHashWithA30DayTTL(t *testing.T) {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		t.Skip("REDIS_ADDR is not set; run `make test-integration`")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	client, err := db.ConnectRedis(ctx, addr, "")
	if err != nil {
		t.Fatalf("connect redis: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	store := NewRedisStore(client)
	user := "it-cart-" + time.Now().Format("150405.000000")
	t.Cleanup(func() { client.Del(ctx, redisKey(user)) })

	if err := store.SetMany(ctx, user, map[string]int64{"SKU-A": 2, "SKU-B": 1}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetMany(ctx, user, map[string]int64{"SKU-A": 5}); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, user)
	if err != nil || got["SKU-A"] != 5 || got["SKU-B"] != 1 || len(got) != 2 {
		t.Fatalf("cart = %v, err = %v", got, err)
	}
	ttl := client.TTL(ctx, redisKey(user)).Val()
	if ttl <= 29*24*time.Hour || ttl > TTL {
		t.Fatalf("ttl = %v; want about 30 days", ttl)
	}
	if err := store.Remove(ctx, user, "SKU-A"); err != nil {
		t.Fatal(err)
	}
	if got, _ = store.Get(ctx, user); len(got) != 1 {
		t.Fatalf("after remove: %v", got)
	}
	other, _ := store.Get(ctx, user+"-other")
	if len(other) != 0 {
		t.Fatalf("another user's cart must be empty: %v", other)
	}
}
