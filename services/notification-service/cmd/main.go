package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/config"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/db"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/eventconsumer"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/middleware"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/migrate"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/outbox"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
	"github.com/hieuvu0611/commercelab-retail-be/services/notification-service/internal/domain"
	"github.com/hieuvu0611/commercelab-retail-be/services/notification-service/internal/stopalert"
	"github.com/hieuvu0611/commercelab-retail-be/services/notification-service/migrations"
)

// consumerGroup is the consumer group of this service on the shipment topic.
const consumerGroup = "notification-service"

func main() {
	port := config.EnvOrDefault("NOTIFICATION_PORT", "8089")
	pgURL := config.EnvOrDefault("DATABASE_URL_NOTIFICATIONS", "postgres://notify_svc_user:notify_secret@localhost:5432/commerce_lab?sslmode=disable&search_path=notifications")
	auth.LoadSecrets(config.NewLoader())

	ctx := context.Background()
	if migrate.EnabledOnStart() {
		if _, err := migrate.Up(ctx, pgURL, migrations.FS); err != nil {
			log.Fatalf("[notification-service] migrations failed: %v", err)
		}
	}
	pool, pgErr := db.ConnectPostgres(ctx, pgURL)
	if err := dependency.CheckStartup("notification-service", "postgres", pgErr); err != nil {
		log.Fatal(err)
	}
	dispatcher := domain.NewDispatcher()
	if pool != nil {
		defer pool.Close()
		dispatcher = domain.NewDispatcherWithStore(domain.NewPostgresStore(pool))
	}

	// Shipment events feed the warehouse stop alerts (SC06).
	hub := stopalert.NewHub()
	brokers := config.EnvOrDefault("REDPANDA_BROKERS", "localhost:19092")
	go func() {
		err := eventconsumer.Run(context.Background(), eventconsumer.Config{Brokers: brokers, Group: consumerGroup, Topic: outbox.TopicFor("Shipment")}, hub.HandleEvent)
		if err != nil {
			log.Printf("[notification-service] shipment event consumer stopped: %v", err)
		}
	}()

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
	registerStopAlertRoutes(mux, hub, defaultKeepAlive)

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
		unread := dispatcher.UnreadCount(recipientID)
		response.JSON(w, http.StatusOK, "Lấy hộp thư thông báo thành công", map[string]any{
			"items":       items,
			"unreadCount": unread,
		}, &response.Metadata{
			Total:     int64(len(items)),
			Page:      1,
			PageCount: 1,
			Limit:     50,
		})
	})

	mux.HandleFunc("PATCH /api/v1/notifications/{recipientId}/{notificationId}/read", func(w http.ResponseWriter, r *http.Request) {
		recipientID := r.PathValue("recipientId")
		notificationID := r.PathValue("notificationId")
		found := dispatcher.MarkAsRead(recipientID, notificationID)
		if !found {
			response.Error(w, r, http.StatusNotFound, "NOT_FOUND", "Không tìm thấy thông báo", nil)
			return
		}
		response.JSON(w, http.StatusOK, "Đã đánh dấu thông báo là đã đọc", map[string]any{
			"notificationId": notificationID,
			"isRead":         true,
			"unreadCount":    dispatcher.UnreadCount(recipientID),
		}, nil)
	})

	mux.HandleFunc("PATCH /api/v1/notifications/{recipientId}/read-all", func(w http.ResponseWriter, r *http.Request) {
		recipientID := r.PathValue("recipientId")
		count := dispatcher.MarkAllAsRead(recipientID)
		response.JSON(w, http.StatusOK, "Đã đánh dấu tất cả thông báo là đã đọc", map[string]any{
			"updatedCount": count,
			"unreadCount":  0,
		}, nil)
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
		w.Header().Set("X-Accel-Buffering", "no")

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
			return
		}

		fmt.Fprintf(w, "event: connected\ndata: {\"recipientId\":%q,\"timestamp\":%q}\n\n", recipientID, time.Now().UTC().Format(time.RFC3339))
		flusher.Flush()

		ch, unsub := dispatcher.Subscribe(recipientID)
		defer unsub()

		for {
			select {
			case <-r.Context().Done():
				return
			case noti, ok := <-ch:
				if !ok {
					return
				}
				data, err := json.Marshal(noti)
				if err == nil {
					fmt.Fprintf(w, "event: notification\ndata: %s\n\n", data)
					flusher.Flush()
				}
			}
		}
	})

	log.Printf("[notification-service] listening on :%s (schema: notifications, mongo: notify_user_inboxes)", port)
	log.Fatal(http.ListenAndServe(":"+port, middleware.Chain(mux)))
}
