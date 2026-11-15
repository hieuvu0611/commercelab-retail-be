//go:build integration

package orders

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/db"
)

func TestPostgresStoreContract(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL_ORDERS")
	if dsn == "" {
		t.Skip("DATABASE_URL_ORDERS is not set; run `make test-integration`")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	pool, err := db.ConnectPostgres(ctx, dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	t.Cleanup(pool.Close)

	run := fmt.Sprintf("it%d", time.Now().UnixNano())
	var n atomic.Int64
	uniq := func(p string) string { return fmt.Sprintf("%s-%s-%d", p, run, n.Add(1)) }
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM orders.idempotency_keys WHERE order_id LIKE '%'||$1||'%'`, run)
		_, _ = pool.Exec(bg, `DELETE FROM orders.orders WHERE order_id LIKE '%'||$1||'%'`, run)
	})
	runStoreContract(t, func(*testing.T) Store { return NewPostgresStore(pool) }, uniq)
	runCaptureContract(t, func(*testing.T) Store { return NewPostgresStore(pool) }, uniq)
	runOutboxContract(t, func(*testing.T) Store { return NewPostgresStore(pool) }, uniq, func(_ Store, orderID string) []string {
		rows, err := pool.Query(ctx, `SELECT event_type FROM orders.outbox_events WHERE aggregate_id = $1 ORDER BY id`, orderID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var typ string
			_ = rows.Scan(&typ)
			out = append(out, typ)
		}
		return out
	})
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM orders.outbox_events WHERE aggregate_id LIKE '%'||$1||'%'`, run)
	})
	runQueryContract(t, func(*testing.T) Store { return NewPostgresStore(pool) }, uniq)
}
