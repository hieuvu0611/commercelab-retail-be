package domain

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/contracts/events"
)

// NotificationItem represents a notification message stored in the user's inbox.
type NotificationItem struct {
	NotificationID string         `json:"notificationId"`
	EventID        string         `json:"eventId"`
	RecipientID    string         `json:"recipientId"`
	Channel        string         `json:"channel"` // IN_APP, SSE, EMAIL, SMS
	Category       string         `json:"category"`
	Title          string         `json:"title"`
	Message        string         `json:"message"`
	IsRead         bool           `json:"isRead"`
	Metadata       map[string]any `json:"metadata,omitempty"`
	CreatedAt      time.Time      `json:"createdAt"`
}

type Dispatcher struct {
	mu          sync.Mutex
	store       InboxStore
	dedup       map[string]bool
	erased      map[string]bool
	inboxes     map[string][]NotificationItem
	subscribers map[string][]chan NotificationItem
}

func NewDispatcher() *Dispatcher {
	return &Dispatcher{
		dedup:       make(map[string]bool),
		erased:      make(map[string]bool),
		inboxes:     make(map[string][]NotificationItem),
		subscribers: make(map[string][]chan NotificationItem),
	}
}

// NewDispatcherWithStore creates a Dispatcher backed by a persistent InboxStore.
func NewDispatcherWithStore(store InboxStore) *Dispatcher {
	d := NewDispatcher()
	d.store = store
	return d
}

// Dispatch saves and broadcasts a notification, strictly enforcing idempotency per (eventId + recipientId + channel).
func (d *Dispatcher) Dispatch(n NotificationItem) (NotificationItem, bool) {
	n = normalizeItem(n)

	if d.store != nil {
		saved, dup, err := d.store.InsertIdempotent(context.Background(), n)
		if err != nil {
			log.Printf("[notification-service] store dispatch failed: %v", err)
			return n, false
		}
		if dup {
			return saved, true
		}
		d.broadcast(saved)
		return saved, false
	}

	d.mu.Lock()
	if d.erased[n.RecipientID] {
		d.mu.Unlock()
		return n, true
	}
	dedupKey := n.EventID + "|" + n.RecipientID + "|" + n.Channel
	if d.dedup[dedupKey] {
		d.mu.Unlock()
		return n, true // Already dispatched, drop duplicate to avoid spamming
	}
	d.dedup[dedupKey] = true
	d.inboxes[n.RecipientID] = append([]NotificationItem{n}, d.inboxes[n.RecipientID]...)
	d.mu.Unlock()

	d.broadcast(n)
	return n, false
}

func (d *Dispatcher) EraseUser(ctx context.Context, userID string, erasedAt, retentionUntil time.Time) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil
	}
	if erasedAt.IsZero() {
		erasedAt = time.Now().UTC()
	} else {
		erasedAt = erasedAt.UTC()
	}
	if retentionUntil.IsZero() {
		retentionUntil = erasedAt.AddDate(10, 0, 0)
	} else {
		retentionUntil = retentionUntil.UTC()
	}

	if d.store != nil {
		if err := d.store.EraseUser(ctx, userID, erasedAt, retentionUntil); err != nil {
			return err
		}
	}

	d.mu.Lock()
	d.erased[userID] = true
	delete(d.inboxes, userID)
	if subs, ok := d.subscribers[userID]; ok {
		for _, ch := range subs {
			close(ch)
		}
		delete(d.subscribers, userID)
	}
	d.mu.Unlock()
	return nil
}

func (d *Dispatcher) HandlePrivacyEvent(ctx context.Context, e events.DomainEvent) error {
	if e.EventType != events.EventUserPIIErased {
		return nil
	}

	userID := strings.TrimSpace(payloadString(e.Payload, "userId"))
	if userID == "" {
		userID = strings.TrimSpace(e.AggregateID)
	}
	if userID == "" {
		log.Printf("[notification-service] skipping %s (%s): missing userId", e.EventType, e.EventID)
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

	return d.EraseUser(ctx, userID, erasedAt, retentionUntil)
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

func (d *Dispatcher) broadcast(n NotificationItem) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.erased[n.RecipientID] {
		return
	}
	if subs, ok := d.subscribers[n.RecipientID]; ok {
		for _, ch := range subs {
			select {
			case ch <- n:
			default:
			}
		}
	}
}

// Subscribe returns a channel receiving real-time notifications for the given recipient, and an unsubscribe cleanup function.
func (d *Dispatcher) Subscribe(recipientID string) (<-chan NotificationItem, func()) {
	d.mu.Lock()
	defer d.mu.Unlock()

	ch := make(chan NotificationItem, 32)
	d.subscribers[recipientID] = append(d.subscribers[recipientID], ch)

	unsub := func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		subs := d.subscribers[recipientID]
		for i, sub := range subs {
			if sub == ch {
				d.subscribers[recipientID] = append(subs[:i], subs[i+1:]...)
				close(ch)
				break
			}
		}
		if len(d.subscribers[recipientID]) == 0 {
			delete(d.subscribers, recipientID)
		}
	}

	return ch, unsub
}

func (d *Dispatcher) ListInbox(recipientID string) []NotificationItem {
	if d.store != nil {
		items, err := d.store.ListInbox(context.Background(), recipientID)
		if err != nil {
			log.Printf("[notification-service] list inbox failed: %v", err)
			return []NotificationItem{}
		}
		return items
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	items := d.inboxes[recipientID]
	out := make([]NotificationItem, len(items))
	copy(out, items)
	return out
}

func (d *Dispatcher) UnreadCount(recipientID string) int {
	if d.store != nil {
		cnt, err := d.store.UnreadCount(context.Background(), recipientID)
		if err != nil {
			log.Printf("[notification-service] unread count failed: %v", err)
			return 0
		}
		return cnt
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	count := 0
	for _, item := range d.inboxes[recipientID] {
		if !item.IsRead {
			count++
		}
	}
	return count
}

func (d *Dispatcher) MarkAsRead(recipientID, notificationID string) bool {
	if d.store != nil {
		ok, err := d.store.MarkAsRead(context.Background(), recipientID, notificationID)
		if err != nil {
			log.Printf("[notification-service] mark read failed: %v", err)
			return false
		}
		return ok
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	items := d.inboxes[recipientID]
	for i := range items {
		if items[i].NotificationID == notificationID {
			items[i].IsRead = true
			return true
		}
	}
	return false
}

func (d *Dispatcher) MarkAllAsRead(recipientID string) int {
	if d.store != nil {
		cnt, err := d.store.MarkAllAsRead(context.Background(), recipientID)
		if err != nil {
			log.Printf("[notification-service] mark all read failed: %v", err)
			return 0
		}
		return cnt
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	count := 0
	items := d.inboxes[recipientID]
	for i := range items {
		if !items[i].IsRead {
			items[i].IsRead = true
			count++
		}
	}
	return count
}
