package domain

import "testing"

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

	// Kafka gửi lặp lại cùng event -> không được nhân đôi thông báo trong hộp thư
	_, duplicate = d.Dispatch(item)
	if !duplicate {
		t.Fatalf("expected second dispatch of same eventId to be deduplicated")
	}
	if len(d.ListInbox("user-lan")) != 1 {
		t.Fatalf("expected exactly 1 notification in inbox, got %d", len(d.ListInbox("user-lan")))
	}
}
