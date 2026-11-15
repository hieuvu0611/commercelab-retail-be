//go:build integration

package outbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/db"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/twmb/franz-go/pkg/kgo"
)

type recorder struct {
	mu      sync.Mutex
	ids     []string
	failAt  string // event id whose publish fails
	headers map[string]string
}

func (r *recorder) Publish(_ context.Context, _, _ string, value []byte, headers map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := headers["event-id"]
	if id == r.failAt {
		return errors.New("broker down")
	}
	r.ids = append(r.ids, id)
	r.headers = headers
	return nil
}

// outboxTable creates a private copy of the outbox table so these tests never claim other tests' rows.
func outboxTable(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
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
	table := fmt.Sprintf("orders.outbox_it_%d", time.Now().UnixNano())
	if _, err := pool.Exec(ctx, `CREATE TABLE `+table+` (LIKE orders.outbox_events INCLUDING ALL)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DROP TABLE IF EXISTS `+table) })
	return pool, table
}

func seed(t *testing.T, pool *pgxpool.Pool, table string, n int) []string {
	t.Helper()
	var ids []string
	for i := 0; i < n; i++ {
		e := NewEvent("order.created.v1", "Order", fmt.Sprintf("O-%d", i), time.Now(), map[string]any{"i": i})
		if err := Insert(context.Background(), pool, table, e); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, e.EventID)
	}
	return ids
}

func TestRelayPublishesPendingRowsInOrderExactlyOnce(t *testing.T) {
	pool, table := outboxTable(t)
	ids := seed(t, pool, table, 3)
	rec := &recorder{}
	relay := NewRelay(pool, table, rec)
	n, err := relay.RunOnce(context.Background())
	if err != nil || n != 3 {
		t.Fatalf("RunOnce = %d, %v", n, err)
	}
	if fmt.Sprint(rec.ids) != fmt.Sprint(ids) || rec.headers["event-type"] != "order.created.v1" {
		t.Fatalf("published %v headers %v, want %v", rec.ids, rec.headers, ids)
	}
	if n, _ := relay.RunOnce(context.Background()); n != 0 || len(rec.ids) != 3 {
		t.Fatalf("second pass published %d more", n)
	}
}

func TestRelayKeepsAFailedRowPendingAndKeepsTheOrder(t *testing.T) {
	pool, table := outboxTable(t)
	ids := seed(t, pool, table, 3)
	rec := &recorder{failAt: ids[1]}
	relay := NewRelay(pool, table, rec)
	n, err := relay.RunOnce(context.Background())
	if err == nil || n != 1 || len(rec.ids) != 1 {
		t.Fatalf("RunOnce = %d, %v published=%v", n, err, rec.ids)
	}
	var attempts int
	var status string
	if err := pool.QueryRow(context.Background(), `SELECT status, attempts FROM `+table+` WHERE event_id = $1`, ids[1]).Scan(&status, &attempts); err != nil || status != "PENDING" || attempts != 1 {
		t.Fatalf("failed row = %s attempts=%d %v", status, attempts, err)
	}
	rec.failAt = ""
	if n, err := relay.RunOnce(context.Background()); err != nil || n != 2 {
		t.Fatalf("retry = %d, %v", n, err)
	}
	if fmt.Sprint(rec.ids) != fmt.Sprint(ids) {
		t.Fatalf("published %v, want %v in order", rec.ids, ids)
	}
}

func TestTwoRelaysNeverPublishARowTwice(t *testing.T) {
	pool, table := outboxTable(t)
	seed(t, pool, table, 40)
	a, b := &recorder{}, &recorder{}
	ra, rb := NewRelay(pool, table, a), NewRelay(pool, table, b)
	ra.batch, rb.batch = 7, 7
	var wg sync.WaitGroup
	for _, r := range []*Relay{ra, rb} {
		wg.Add(1)
		go func(r *Relay) {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				if _, err := r.RunOnce(context.Background()); err != nil {
					t.Error(err)
					return
				}
			}
		}(r)
	}
	wg.Wait()
	seen := map[string]int{}
	for _, id := range append(append([]string{}, a.ids...), b.ids...) {
		seen[id]++
	}
	for id, c := range seen {
		if c != 1 {
			t.Fatalf("event %s published %d times", id, c)
		}
	}
	if len(seen) != 40 {
		t.Fatalf("published %d of 40 events", len(seen))
	}
}

func TestKafkaPublisherDeliversToRedpandaWithKeyAndHeaders(t *testing.T) {
	brokers := os.Getenv("REDPANDA_BROKERS")
	if brokers == "" {
		t.Skip("REDPANDA_BROKERS is not set; run `make test-integration`")
	}
	pool, table := outboxTable(t)
	ids := seed(t, pool, table, 3)
	pub, err := NewKafkaPublisher(brokers)
	if err != nil {
		t.Fatal(err)
	}
	defer pub.Close()
	if n, err := NewRelay(pool, table, pub).RunOnce(context.Background()); err != nil || n != 3 {
		t.Fatalf("RunOnce = %d, %v", n, err)
	}

	consumer, err := kgo.NewClient(kgo.SeedBrokers(brokers), kgo.ConsumeTopics(TopicFor("Order")), kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()))
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	want := map[string]bool{ids[0]: true, ids[1]: true, ids[2]: true}
	for len(want) > 0 {
		fetches := consumer.PollFetches(ctx)
		if ctx.Err() != nil {
			t.Fatalf("timed out waiting for %d events", len(want))
		}
		fetches.EachRecord(func(r *kgo.Record) {
			for _, h := range r.Headers {
				if h.Key == "event-id" && want[string(h.Value)] {
					if string(r.Key) == "" {
						t.Errorf("record without a key")
					}
					delete(want, string(h.Value))
				}
			}
		})
	}
}
