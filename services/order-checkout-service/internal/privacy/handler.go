package privacy

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/contracts/events"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/snapshot"
)

type Handler struct {
	Tombstones Store
	Snapshots  snapshot.Store
}

func (h *Handler) Handle(ctx context.Context, e events.DomainEvent) error {
	if e.EventType != events.EventUserPIIErased {
		return nil
	}

	userID := strings.TrimSpace(payloadString(e.Payload, "userId"))
	if userID == "" {
		userID = strings.TrimSpace(e.AggregateID)
	}
	if userID == "" {
		log.Printf("[order-privacy] skipping %s (%s): missing userId", e.EventType, e.EventID)
		return nil
	}

	erasedAt := payloadTime(e.Payload, "erasedAt")
	if erasedAt.IsZero() {
		if !e.OccurredAt.IsZero() {
			erasedAt = e.OccurredAt.UTC()
		} else {
			erasedAt = time.Now().UTC()
		}
	}

	retentionUntil := payloadTime(e.Payload, "retentionUntil")
	if retentionUntil.IsZero() {
		retentionUntil = erasedAt.AddDate(10, 0, 0)
	}

	if h.Tombstones != nil {
		if err := h.Tombstones.Record(ctx, Tombstone{
			UserID:         userID,
			ErasedAt:       erasedAt,
			RetentionUntil: retentionUntil,
		}); err != nil {
			return err
		}
	}

	if h.Snapshots != nil {
		if _, err := h.Snapshots.RedactUserPII(ctx, userID, erasedAt); err != nil {
			return err
		}
	}

	return nil
}

func payloadString(p map[string]any, key string) string {
	if p == nil {
		return ""
	}
	v, _ := p[key].(string)
	return v
}

func payloadTime(p map[string]any, key string) time.Time {
	if p == nil {
		return time.Time{}
	}
	switch v := p[key].(type) {
	case time.Time:
		return v.UTC()
	case string:
		v = strings.TrimSpace(v)
		if v == "" {
			return time.Time{}
		}
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			return t.UTC()
		}
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}
