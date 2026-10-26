package domain

import (
	"sync"
	"time"
)

// NotificationItem lưu trong MongoDB notify_user_inboxes (có TTL Index 90 ngày)
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
	mu      sync.Mutex
	dedup   map[string]bool
	inboxes map[string][]NotificationItem
}

func NewDispatcher() *Dispatcher {
	return &Dispatcher{
		dedup:   make(map[string]bool),
		inboxes: make(map[string][]NotificationItem),
	}
}

// Dispatch tạo thông báo chống trùng lặp theo (eventId + recipientId + channel)
func (d *Dispatcher) Dispatch(n NotificationItem) (NotificationItem, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	dedupKey := n.EventID + "|" + n.RecipientID + "|" + n.Channel
	if d.dedup[dedupKey] {
		return n, true // Đã gửi trước đó -> bỏ qua không spam khách
	}
	d.dedup[dedupKey] = true
	if n.CreatedAt.IsZero() {
		n.CreatedAt = time.Now().UTC()
	}
	d.inboxes[n.RecipientID] = append([]NotificationItem{n}, d.inboxes[n.RecipientID]...)
	return n, false
}

func (d *Dispatcher) ListInbox(recipientID string) []NotificationItem {
	d.mu.Lock()
	defer d.mu.Unlock()
	items := d.inboxes[recipientID]
	out := make([]NotificationItem, len(items))
	copy(out, items)
	return out
}
