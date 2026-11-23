//go:build integration

package domain

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/db"
)

func TestPostgresStoreEraseUserPurgesInboxAndSuppressesReplay(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL_NOTIFICATIONS")
	if dsn == "" {
		t.Skip("DATABASE_URL_NOTIFICATIONS is not set; run `make test-integration`")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	pool, err := db.ConnectPostgres(ctx, dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	t.Cleanup(pool.Close)

	store := NewPostgresStore(pool)
	userID := fmt.Sprintf("usr-notify-pii-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM notifications.inbox_items WHERE recipient_id = $1`, userID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM notifications.inbox_dedup_deliveries WHERE recipient_id = $1`, userID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM notifications.pii_erasure_tombstones WHERE user_id = $1`, userID)
	})

	item := normalizeItem(NotificationItem{
		NotificationID: fmt.Sprintf("NOTI-BEFORE-%d", time.Now().UnixNano()),
		EventID:        fmt.Sprintf("evt-before-%d", time.Now().UnixNano()),
		RecipientID:    userID,
		Channel:        "IN_APP",
		Category:       "ORDER_CONFIRMED",
		Title:          "Order confirmed",
		Message:        "Your order has been confirmed.",
	})
	if _, dup, err := store.InsertIdempotent(ctx, item); err != nil || dup {
		t.Fatalf("insert before erasure: dup=%v err=%v", dup, err)
	}

	erasedAt := time.Now().UTC().Truncate(time.Second)
	if err := store.EraseUser(ctx, userID, erasedAt, erasedAt.AddDate(10, 0, 0)); err != nil {
		t.Fatalf("erase user: %v", err)
	}

	erased, err := store.IsUserErased(ctx, userID)
	if err != nil || !erased {
		t.Fatalf("expected user tombstone, erased=%v err=%v", erased, err)
	}

	items, err := store.ListInbox(ctx, userID)
	if err != nil {
		t.Fatalf("list inbox: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected purged inbox, got %d items", len(items))
	}

	replayed := normalizeItem(NotificationItem{
		NotificationID: fmt.Sprintf("NOTI-REPLAY-%d", time.Now().UnixNano()),
		EventID:        fmt.Sprintf("evt-replay-%d", time.Now().UnixNano()),
		RecipientID:    userID,
		Channel:        "IN_APP",
		Category:       "ORDER_DELIVERED",
		Title:          "Order delivered",
		Message:        "Historical replay event.",
	})
	_, suppressed, err := store.InsertIdempotent(ctx, replayed)
	if err != nil || !suppressed {
		t.Fatalf("replayed notification must be suppressed: suppressed=%v err=%v", suppressed, err)
	}

	itemsAfter, err := store.ListInbox(ctx, userID)
	if err != nil {
		t.Fatalf("list inbox after replay: %v", err)
	}
	if len(itemsAfter) != 0 {
		t.Fatalf("replayed event resurrected inbox items: %+v", itemsAfter)
	}
}
