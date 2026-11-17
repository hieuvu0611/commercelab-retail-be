package domain

import (
	"sync"
	"time"
)

// NotificationItem represents a notification message stored in MongoDB notify_user_inboxes (with 90-day TTL).
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
	dedup       map[string]bool
	inboxes     map[string][]NotificationItem
	subscribers map[string][]chan NotificationItem
}

func NewDispatcher() *Dispatcher {
	return &Dispatcher{
		dedup:       make(map[string]bool),
		inboxes:     make(map[string][]NotificationItem),
		subscribers: make(map[string][]chan NotificationItem),
	}
}

// Dispatch saves and broadcasts a notification, strictly enforcing idempotency per (eventId + recipientId + channel).
func (d *Dispatcher) Dispatch(n NotificationItem) (NotificationItem, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	dedupKey := n.EventID + "|" + n.RecipientID + "|" + n.Channel
	if d.dedup[dedupKey] {
		return n, true // Already dispatched, drop duplicate to avoid spamming
	}
	d.dedup[dedupKey] = true

	if n.NotificationID == "" {
		n.NotificationID = "NOTI-" + time.Now().Format("20060102150405") + "-" + n.EventID
	}
	if n.CreatedAt.IsZero() {
		n.CreatedAt = time.Now().UTC()
	}

	d.inboxes[n.RecipientID] = append([]NotificationItem{n}, d.inboxes[n.RecipientID]...)

	// Broadcast to active SSE subscribers for this recipient
	if subs, ok := d.subscribers[n.RecipientID]; ok {
		for _, ch := range subs {
			select {
			case ch <- n:
			default:
				// Skip if buffer is full to prevent blocking
			}
		}
	}

	return n, false
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
	d.mu.Lock()
	defer d.mu.Unlock()
	items := d.inboxes[recipientID]
	out := make([]NotificationItem, len(items))
	copy(out, items)
	return out
}

func (d *Dispatcher) UnreadCount(recipientID string) int {
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
