//go:build integration

package attempt

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/db"
)

func TestPostgresStoreContractAndLateCapture(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL_PAYMENTS")
	if dsn == "" {
		t.Skip("DATABASE_URL_PAYMENTS is not set; run `make test-integration`")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	pool, err := db.ConnectPostgres(ctx, dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	t.Cleanup(pool.Close)

	run := fmt.Sprintf("it%d", time.Now().UnixNano()%1_000_000_000)
	var n atomic.Int64
	uniq := func(p string) string { return fmt.Sprintf("%s-%s-%d", p, run, n.Add(1)) }
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM payments.webhook_events WHERE attempt_id LIKE '%'||$1||'%'`, run)
		_, _ = pool.Exec(bg, `DELETE FROM payments.refund_obligations WHERE order_id LIKE '%'||$1||'%'`, run)
		_, _ = pool.Exec(bg, `DELETE FROM payments.payment_attempts WHERE order_id LIKE '%'||$1||'%'`, run)
		_, _ = pool.Exec(bg, `DELETE FROM payments.order_payment_summary WHERE order_id LIKE '%'||$1||'%'`, run)
	})
	store := NewPostgresStore(pool)
	runStoreContract(t, store, uniq)
	runOutboxContract(t, store, uniq, func(orderID string) []string {
		rows, err := pool.Query(ctx, `SELECT event_type FROM payments.outbox_events WHERE aggregate_id = $1 ORDER BY id`, orderID)
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
		_, _ = pool.Exec(context.Background(), `DELETE FROM payments.outbox_events WHERE aggregate_id LIKE '%'||$1||'%'`, run)
	})
	runLateCaptureContract(t, store, uniq, func(attemptID string) {
		if _, err := pool.Exec(ctx, `UPDATE payments.payment_attempts SET unknown_deadline = NOW() - INTERVAL '1 minute' WHERE attempt_id = $1`, attemptID); err != nil {
			t.Fatal(err)
		}
	})
}
