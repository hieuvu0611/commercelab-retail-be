//go:build integration

package ratelimit

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/db"
)

func integrationRedis(t *testing.T, window time.Duration) (*Redis, context.Context) {
	t.Helper()
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		t.Skip("REDIS_ADDR is not set; run `make test-integration`")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	client, err := db.ConnectRedis(ctx, addr, "")
	if err != nil {
		t.Fatalf("connect redis: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return NewRedis(client, MaxFailures, window), ctx
}

func uniqueKey() string { return fmt.Sprintf("rl:test:%d", time.Now().UnixNano()) }

func TestRedisBlocksAfterFiveFailuresAndResetClears(t *testing.T) {
	l, ctx := integrationRedis(t, time.Minute)
	key := uniqueKey()

	for i := 0; i < MaxFailures; i++ {
		if blocked, _, err := l.Blocked(ctx, key); err != nil || blocked {
			t.Fatalf("attempt %d must be allowed (blocked=%v err=%v)", i+1, blocked, err)
		}
		if err := l.RecordFailure(ctx, key); err != nil {
			t.Fatal(err)
		}
	}
	blocked, retryAfter, err := l.Blocked(ctx, key)
	if err != nil || !blocked || retryAfter <= 0 || retryAfter > time.Minute {
		t.Fatalf("blocked=%v retryAfter=%v err=%v; want blocked within the window", blocked, retryAfter, err)
	}
	if err := l.Reset(ctx, key); err != nil {
		t.Fatal(err)
	}
	if blocked, _, _ := l.Blocked(ctx, key); blocked {
		t.Fatal("reset must clear the block")
	}
}

func TestRedisWindowExpires(t *testing.T) {
	l, ctx := integrationRedis(t, 1500*time.Millisecond)
	key := uniqueKey()
	for i := 0; i < MaxFailures; i++ {
		_ = l.RecordFailure(ctx, key)
	}
	if blocked, _, _ := l.Blocked(ctx, key); !blocked {
		t.Fatal("must be blocked inside the window")
	}
	time.Sleep(2 * time.Second)
	if blocked, _, _ := l.Blocked(ctx, key); blocked {
		t.Fatal("must be allowed once the window expired")
	}
}

func TestRedisConcurrentFailuresAreAllCounted(t *testing.T) {
	l, ctx := integrationRedis(t, time.Minute)
	key := uniqueKey()
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func() {
			_ = l.RecordFailure(ctx, key)
			done <- struct{}{}
		}()
	}
	for i := 0; i < 8; i++ {
		<-done
	}
	if blocked, _, _ := l.Blocked(ctx, key); !blocked {
		t.Fatal("8 concurrent failures must exceed the limit of 5")
	}
}
