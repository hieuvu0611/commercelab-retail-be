// Package outbox implements the transactional outbox (docs/04 7.5): a service writes an event row in the same
// SQL transaction as the state change it announces, and a relay publishes the rows to Redpanda afterwards.
// Delivery is at least once and in row order per service; consumers dedupe by the event id.
package outbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/contracts/events"
	"github.com/jackc/pgx/v5/pgconn"
)

// Tables of the services that own an outbox.
const (
	IAMTable      = "iam.outbox_events"
	OrdersTable   = "orders.outbox_events"
	PaymentsTable = "payments.outbox_events"
)

// Execer is a pgx transaction or pool.
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// NewEvent builds an event with a fresh random id. The payload must carry no personal data (no names, phones,
// addresses or tokens): events leave the service.
func NewEvent(eventType, aggregateType, aggregateID string, at time.Time, payload map[string]any) events.DomainEvent {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // the OS random source failing is not recoverable
	}
	return events.DomainEvent{
		EventID: "evt-" + hex.EncodeToString(b[:]), EventType: eventType, AggregateType: aggregateType,
		AggregateID: aggregateID, OccurredAt: at.UTC(), Payload: payload,
	}
}

// Insert writes the event into table (for example OrdersTable) inside the caller's transaction.
func Insert(ctx context.Context, q Execer, table string, e events.DomainEvent) error {
	payload, err := json.Marshal(e.Payload)
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx, `INSERT INTO `+table+` (event_id, event_type, aggregate_type, aggregate_id, payload, occurred_at)
		VALUES ($1, $2, $3, $4, $5, $6)`, e.EventID, e.EventType, e.AggregateType, e.AggregateID, payload, e.OccurredAt)
	return err
}

// TopicFor names the topic of an aggregate type: one topic per aggregate, so events of one order stay in order.
func TopicFor(aggregateType string) string {
	return "commercelab." + strings.ToLower(aggregateType) + ".events"
}
