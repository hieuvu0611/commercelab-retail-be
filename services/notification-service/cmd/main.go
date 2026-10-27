package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/config"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/middleware"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
	"github.com/hieuvu0611/commercelab-retail-be/services/notification-service/internal/domain"
)

func main() {
	port := config.EnvOrDefault("NOTIFICATION_PORT", "8089")
	dispatcher := domain.NewDispatcher()

	// Seed thông báo mẫu cho khách hàng Lan và nhân viên Kho Minh
	dispatcher.Dispatch(domain.NotificationItem{
		NotificationID: "NOTI-INIT-01",
		EventID:        "evt-seed-01",
		RecipientID:    "user-lan",
		Channel:        "IN_APP",
		Category:       "ORDER_CONFIRMED",
		Title:          "Đơn hàng O-001 đã xác nhận",
		Message:        "Kho WH-HCM-01 đang đóng gói 2 Balo City + 1 Bình nước của bạn.",
	})
	dispatcher.Dispatch(domain.NotificationItem{
		NotificationID: "NOTI-INIT-02",
		EventID:        "evt-seed-02",
		RecipientID:    "warehouse-minh",
		Channel:        "IN_APP",
		Category:       "WAREHOUSE_STOP_SIGNAL",
		Title:          "Cảnh báo: Yêu cầu dừng đóng gói kiện S-001",
		Message:        "Khách hàng vừa gửi yêu cầu hủy đơn O-001. Vui lòng xác nhận dừng trước khi bàn giao bưu tá (SC06).",
	})

	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		response.JSON(w, http.StatusOK, "notification-service healthy", map[string]any{
			"service":        "notification-service",
			"postgresSchema": "notifications",
			"mongoColl":      "notify_user_inboxes (TTL 90d)",
		}, nil)
	})

	mux.HandleFunc("GET /api/v1/notifications/{recipientId}", func(w http.ResponseWriter, r *http.Request) {
		recipientID := r.PathValue("recipientId")
		items := dispatcher.ListInbox(recipientID)
		response.JSON(w, http.StatusOK, "Lấy hộp thư thông báo thành công", items, &response.Metadata{
			Total:     int64(len(items)),
			Page:      1,
			PageCount: 1,
			Limit:     20,
		})
	})

	mux.HandleFunc("POST /api/v1/notifications/dispatch", func(w http.ResponseWriter, r *http.Request) {
		var req domain.NotificationItem
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.EventID == "" || req.RecipientID == "" {
			response.Error(w, r, http.StatusBadRequest, "INVALID_PAYLOAD", "Thiếu eventId hoặc recipientId", nil)
			return
		}
		if req.Channel == "" {
			req.Channel = "IN_APP"
		}
		item, deduplicated := dispatcher.Dispatch(req)
		msg := "Tạo và đẩy thông báo thành công"
		if deduplicated {
			msg = "Thông báo trùng EventID đã được lọc bỏ (Idempotent Delivery)"
		}
		response.JSON(w, http.StatusOK, msg, map[string]any{
			"notification": item,
			"deduplicated": deduplicated,
		}, nil)
	})

	// SSE (Server-Sent Events) đẩy thông báo real-time lên quả chuông fe/
	mux.HandleFunc("GET /api/v1/notifications/{recipientId}/stream", func(w http.ResponseWriter, r *http.Request) {
		recipientID := r.PathValue("recipientId")
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		fmt.Fprintf(w, "event: connected\ndata: {\"recipientId\":%q,\"timestamp\":%q}\n\n", recipientID, time.Now().UTC().Format(time.RFC3339))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	})

	log.Printf("[notification-service] listening on :%s (schema: notifications, mongo: notify_user_inboxes)", port)
	log.Fatal(http.ListenAndServe(":"+port, middleware.Chain(mux)))
}
