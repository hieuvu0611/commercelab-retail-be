package domain

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/contracts/events"
)

func TestEraseUserPurgesInboxAndClosesSSEStream(t *testing.T) {
	d := NewDispatcher()
	ch, unsub := d.Subscribe("user-lan")
	defer unsub()

	d.Dispatch(NotificationItem{
		NotificationID: "NOTI-ERASE-1",
		EventID:        "evt-order-1",
		RecipientID:    "user-lan",
		Channel:        "IN_APP",
		Category:       "ORDER_CONFIRMED",
		Title:          "Order confirmed",
		Message:        "Your order is confirmed.",
	})
	d.Dispatch(NotificationItem{
		NotificationID: "NOTI-OTHER-1",
		EventID:        "evt-order-2",
		RecipientID:    "user-other",
		Channel:        "IN_APP",
		Category:       "ORDER_CONFIRMED",
		Title:          "Other user order",
		Message:        "Should stay intact.",
	})

	select {
	case msg, ok := <-ch:
		if !ok || msg.NotificationID != "NOTI-ERASE-1" {
			t.Fatalf("expected initial SSE notification before erasure, got ok=%v msg=%+v", ok, msg)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for pre-erasure SSE notification")
	}

	erasedAt := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	if err := d.EraseUser(context.Background(), "user-lan", erasedAt, erasedAt.AddDate(10, 0, 0)); err != nil {
		t.Fatalf("EraseUser: %v", err)
	}

	if items := d.ListInbox("user-lan"); len(items) != 0 {
		t.Fatalf("expected 0 inbox items for erased user, got %d", len(items))
	}
	if unread := d.UnreadCount("user-lan"); unread != 0 {
		t.Fatalf("expected 0 unread count for erased user, got %d", unread)
	}

	select {
	case _, open := <-ch:
		if open {
			t.Fatal("expected SSE channel to be closed after EraseUser")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for SSE channel to close after EraseUser")
	}

	if items := d.ListInbox("user-other"); len(items) != 1 {
		t.Fatalf("expected unrelated user to keep 1 inbox item, got %d", len(items))
	}
}

func TestDispatchAfterErasureSuppressesReplayInboxAndSSE(t *testing.T) {
	d := NewDispatcher()
	erasedAt := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	if err := d.EraseUser(context.Background(), "user-lan", erasedAt, erasedAt.AddDate(10, 0, 0)); err != nil {
		t.Fatalf("EraseUser: %v", err)
	}

	ch, unsub := d.Subscribe("user-lan")
	defer unsub()

	_, dropped := d.Dispatch(NotificationItem{
		NotificationID: "NOTI-REPLAY-1",
		EventID:        "evt-old-order-99",
		RecipientID:    "user-lan",
		Channel:        "IN_APP",
		Category:       "ORDER_CONFIRMED",
		Title:          "Old order confirmed",
		Message:        "Replay must not recreate inbox rows or SSE messages.",
	})
	if !dropped {
		t.Fatal("expected Dispatch for erased user to be suppressed (dropped=true)")
	}
	if items := d.ListInbox("user-lan"); len(items) != 0 {
		t.Fatalf("expected 0 inbox rows after replay dispatch, got %d", len(items))
	}

	select {
	case msg, open := <-ch:
		if open {
			t.Fatalf("expected 0 SSE events for erased user, received %+v", msg)
		}
	case <-time.After(100 * time.Millisecond):
	}
}

func TestHandlePrivacyEventIsIdempotent(t *testing.T) {
	store := newFakeInboxStore()
	d := NewDispatcherWithStore(store)

	d.Dispatch(NotificationItem{
		NotificationID: "NOTI-STORE-1",
		EventID:        "evt-store-1",
		RecipientID:    "user-lan",
		Channel:        "IN_APP",
		Category:       "ORDER_CONFIRMED",
		Title:          "Order confirmed",
		Message:        "Stored before erasure.",
	})

	erasedAt := time.Date(2026, 11, 21, 15, 0, 0, 0, time.UTC)
	ev := events.DomainEvent{
		EventID:       "evt-pii-erase-1",
		EventType:     events.EventUserPIIErased,
		AggregateType: "User",
		AggregateID:   "user-lan",
		OccurredAt:    erasedAt,
		Payload: map[string]any{
			"userId":         "user-lan",
			"erasedAt":       erasedAt.Format(time.RFC3339),
			"retentionUntil": erasedAt.AddDate(10, 0, 0).Format(time.RFC3339),
		},
	}

	if err := d.HandlePrivacyEvent(context.Background(), ev); err != nil {
		t.Fatalf("first HandlePrivacyEvent: %v", err)
	}
	if err := d.HandlePrivacyEvent(context.Background(), ev); err != nil {
		t.Fatalf("duplicate HandlePrivacyEvent: %v", err)
	}

	erased, err := store.IsUserErased(context.Background(), "user-lan")
	if err != nil || !erased {
		t.Fatalf("IsUserErased = %v, err=%v; want true", erased, err)
	}
	if items := d.ListInbox("user-lan"); len(items) != 0 {
		t.Fatalf("expected 0 inbox items in store after erasure, got %d", len(items))
	}

	_, dropped := d.Dispatch(NotificationItem{
		NotificationID: "NOTI-STORE-REPLAY",
		EventID:        "evt-store-replay",
		RecipientID:    "user-lan",
		Channel:        "IN_APP",
		Title:          "Replay",
	})
	if !dropped {
		t.Fatal("expected store-backed Dispatch for erased user to be suppressed")
	}
	if items := d.ListInbox("user-lan"); len(items) != 0 {
		t.Fatalf("expected 0 inbox rows in store after replay, got %d", len(items))
	}
}

type fakeInboxStore struct {
	mu     sync.Mutex
	erased map[string]bool
	dedup  map[string]bool
	items  map[string][]NotificationItem
}

func newFakeInboxStore() *fakeInboxStore {
	return &fakeInboxStore{
		erased: make(map[string]bool),
		dedup:  make(map[string]bool),
		items:  make(map[string][]NotificationItem),
	}
}

func (f *fakeInboxStore) InsertIdempotent(_ context.Context, n NotificationItem) (NotificationItem, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.erased[n.RecipientID] {
		return n, true, nil
	}
	key := n.EventID + "|" + n.RecipientID + "|" + n.Channel
	if f.dedup[key] {
		return n, true, nil
	}
	f.dedup[key] = true
	f.items[n.RecipientID] = append([]NotificationItem{n}, f.items[n.RecipientID]...)
	return n, false, nil
}

func (f *fakeInboxStore) ListInbox(_ context.Context, recipientID string) ([]NotificationItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	src := f.items[recipientID]
	out := make([]NotificationItem, len(src))
	copy(out, src)
	return out, nil
}

func (f *fakeInboxStore) UnreadCount(_ context.Context, recipientID string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cnt := 0
	for _, it := range f.items[recipientID] {
		if !it.IsRead {
			cnt++
		}
	}
	return cnt, nil
}

func (f *fakeInboxStore) MarkAsRead(_ context.Context, recipientID, notificationID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.items[recipientID] {
		if f.items[recipientID][i].NotificationID == notificationID {
			f.items[recipientID][i].IsRead = true
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeInboxStore) MarkAllAsRead(_ context.Context, recipientID string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cnt := 0
	for i := range f.items[recipientID] {
		if !f.items[recipientID][i].IsRead {
			f.items[recipientID][i].IsRead = true
			cnt++
		}
	}
	return cnt, nil
}

func (f *fakeInboxStore) EraseUser(_ context.Context, userID string, _, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.erased[userID] = true
	delete(f.items, userID)
	return nil
}

func (f *fakeInboxStore) IsUserErased(_ context.Context, userID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.erased[userID], nil
}
