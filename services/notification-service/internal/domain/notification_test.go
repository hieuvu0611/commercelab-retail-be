package domain

import (
	"testing"
	"time"
)

func TestIdempotentNotificationDelivery(t *testing.T) {
	d := NewDispatcher()
	item := NotificationItem{
		NotificationID: "NOTI-001",
		EventID:        "evt-order-confirmed-001",
		RecipientID:    "user-lan",
		Channel:        "IN_APP",
		Category:       "ORDER_UPDATE",
		Title:          "Đơn hàng O-001 đã được xác nhận",
		Message:        "Đã thu 660.000đ và cam kết giữ 2 Balo + 1 Bình nước.",
	}

	_, duplicate := d.Dispatch(item)
	if duplicate {
		t.Fatalf("expected first dispatch not to be duplicate")
	}

	// Kafka replay of the same event -> must not duplicate notification in user's inbox
	_, duplicate = d.Dispatch(item)
	if !duplicate {
		t.Fatalf("expected second dispatch of same eventId to be deduplicated")
	}
	if len(d.ListInbox("user-lan")) != 1 {
		t.Fatalf("expected exactly 1 notification in inbox, got %d", len(d.ListInbox("user-lan")))
	}
}

func TestRealTimeNotificationBroadcast(t *testing.T) {
	d := NewDispatcher()

	ch, unsub := d.Subscribe("user-lan")
	defer unsub()

	item := NotificationItem{
		NotificationID: "NOTI-002",
		EventID:        "evt-shipment-packed-001",
		RecipientID:    "user-lan",
		Channel:        "IN_APP",
		Category:       "SHIPMENT_UPDATE",
		Title:          "Kiện hàng S-001 đã đóng gói",
		Message:        "Bưu phẩm đã sẵn sàng bàn giao cho bưu tá",
	}

	_, dup := d.Dispatch(item)
	if dup {
		t.Fatalf("expected first dispatch not to be duplicate")
	}

	select {
	case received := <-ch:
		if received.NotificationID != "NOTI-002" {
			t.Fatalf("expected NOTI-002, got %s", received.NotificationID)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("timed out waiting for real-time notification broadcast")
	}
}

func TestReadStateManagement(t *testing.T) {
	d := NewDispatcher()

	d.Dispatch(NotificationItem{
		NotificationID: "NOTI-101",
		EventID:        "evt-101",
		RecipientID:    "staff-minh",
		Channel:        "IN_APP",
		Title:          "Task 1",
	})
	d.Dispatch(NotificationItem{
		NotificationID: "NOTI-102",
		EventID:        "evt-102",
		RecipientID:    "staff-minh",
		Channel:        "IN_APP",
		Title:          "Task 2",
	})

	if count := d.UnreadCount("staff-minh"); count != 2 {
		t.Fatalf("expected 2 unread notifications, got %d", count)
	}

	found := d.MarkAsRead("staff-minh", "NOTI-101")
	if !found {
		t.Fatalf("expected NOTI-101 to be found and marked as read")
	}

	if count := d.UnreadCount("staff-minh"); count != 1 {
		t.Fatalf("expected 1 unread notification, got %d", count)
	}

	markedAll := d.MarkAllAsRead("staff-minh")
	if markedAll != 1 {
		t.Fatalf("expected 1 remaining unread to be marked, got %d", markedAll)
	}

	if count := d.UnreadCount("staff-minh"); count != 0 {
		t.Fatalf("expected 0 unread notifications, got %d", count)
	}
}
