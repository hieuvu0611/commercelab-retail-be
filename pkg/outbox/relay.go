package outbox

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/contracts/events"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Publisher sends one message to the broker and returns only after the broker accepted it.
type Publisher interface {
	Publish(ctx context.Context, topic, key string, value []byte, headers map[string]string) error
}

// DefaultBatch is how many rows one relay pass claims.
const DefaultBatch = 100

// Relay publishes PENDING rows of one outbox table. Several relays may run on the same table: rows are claimed
// with FOR UPDATE SKIP LOCKED, so each row is published by one relay at a time.
type Relay struct {
	pool  *pgxpool.Pool
	table string
	pub   Publisher
	batch int
}

func NewRelay(pool *pgxpool.Pool, table string, pub Publisher) *Relay {
	return &Relay{pool: pool, table: table, pub: pub, batch: DefaultBatch}
}

// RunOnce publishes up to one batch in id order and returns how many rows were marked PUBLISHED. It stops at the
// first publish failure so the order is kept; the failed row stays PENDING (its attempts counter grows) and is
// retried by the next pass. A row is marked PUBLISHED in the same transaction that claimed it, so a crash after
// publishing but before commit republishes it: delivery is at least once and the event id lets consumers dedupe.
func (r *Relay) RunOnce(ctx context.Context) (int, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, dependency.Wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `SELECT id, event_id, event_type, aggregate_type, aggregate_id, payload, occurred_at FROM `+r.table+`
		WHERE status = 'PENDING' ORDER BY id LIMIT $1 FOR UPDATE SKIP LOCKED`, r.batch)
	if err != nil {
		return 0, dependency.Wrap(err)
	}
	type claimed struct {
		id int64
		e  events.DomainEvent
	}
	var batch []claimed
	for rows.Next() {
		var c claimed
		var payload []byte
		if err := rows.Scan(&c.id, &c.e.EventID, &c.e.EventType, &c.e.AggregateType, &c.e.AggregateID, &payload, &c.e.OccurredAt); err != nil {
			rows.Close()
			return 0, dependency.Wrap(err)
		}
		if err := json.Unmarshal(payload, &c.e.Payload); err != nil {
			rows.Close()
			return 0, err
		}
		batch = append(batch, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, dependency.Wrap(err)
	}

	published := 0
	var publishErr error
	for _, c := range batch {
		value, err := json.Marshal(c.e)
		if err != nil {
			publishErr = err
			break
		}
		err = r.pub.Publish(ctx, TopicFor(c.e.AggregateType), c.e.AggregateID, value,
			map[string]string{"event-id": c.e.EventID, "event-type": c.e.EventType})
		if err != nil {
			publishErr = err
			if _, uerr := tx.Exec(ctx, `UPDATE `+r.table+` SET attempts = attempts + 1 WHERE id = $1`, c.id); uerr != nil {
				return published, dependency.Wrap(uerr)
			}
			break
		}
		if _, err := tx.Exec(ctx, `UPDATE `+r.table+` SET status = 'PUBLISHED', published_at = NOW() WHERE id = $1`, c.id); err != nil {
			return published, dependency.Wrap(err)
		}
		published++
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, dependency.Wrap(err)
	}
	return published, publishErr
}

// Run publishes every interval until ctx is cancelled. Failures are logged and retried on the next tick.
func (r *Relay) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for {
				n, err := r.RunOnce(ctx)
				if err != nil {
					log.Printf("[outbox] %s: publish failed, will retry: %v", r.table, err)
					break
				}
				if n < r.batch {
					break
				}
			}
		}
	}
}
