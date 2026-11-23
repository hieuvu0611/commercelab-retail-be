//go:build integration

package privacy

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/db"
)

func TestPostgresTombstoneStoreRecordsAndChecksIdempotently(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL_ORDERS")
	if dsn == "" {
		t.Skip("DATABASE_URL_ORDERS is not set; run `make test-integration`")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	pool, err := db.ConnectPostgres(ctx, dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	t.Cleanup(pool.Close)

	store := NewPostgresStore(pool)
	userID := fmt.Sprintf("usr-order-tombstone-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM orders.pii_erasure_tombstones WHERE user_id = $1`, userID)
	})

	erased, _, err := store.IsErased(ctx, userID)
	if err != nil || erased {
		t.Fatalf("fresh user should not be erased: erased=%v err=%v", erased, err)
	}

	erasedAt := time.Now().UTC().Truncate(time.Second)
	tb := Tombstone{
		UserID:         userID,
		ErasedAt:       erasedAt,
		RetentionUntil: erasedAt.AddDate(10, 0, 0),
	}
	if err := store.Record(ctx, tb); err != nil {
		t.Fatalf("first record: %v", err)
	}
	if err := store.Record(ctx, tb); err != nil {
		t.Fatalf("second idempotent record: %v", err)
	}

	erased, gotAt, err := store.IsErased(ctx, userID)
	if err != nil || !erased || !gotAt.Equal(erasedAt) {
		t.Fatalf("IsErased = (%v, %v, %v); want (true, %v, nil)", erased, gotAt, err, erasedAt)
	}
}
