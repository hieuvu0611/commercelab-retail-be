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
	"github.com/hieuvu0611/commercelab-retail-be/pkg/internalauth"
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

// NewServer constructs the HTTP handler for notification-service routes.
func NewServer(dispatcher *domain.Dispatcher, hub *stopalert.Hub, keepAlive time.Duration) http.Handler {
	mux := http.NewServeMux()
	registerStopAlertRoutes(mux, hub, keepAlive)

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		response.JSON(w, http.StatusOK, "notification-service healthy", map[string]any{
			"service":        "notification-service",
			"postgresSchema": "notifications",
			"mongoColl":      "notify_user_inboxes (TTL 90d)",
		}, nil)
	})

	requireRecipientOwner := func(next http.HandlerFunc) http.Handler {
		return auth.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := auth.ClaimsFrom(r.Context())
			recipientID := r.PathValue("recipientId")
			if !ok || claims == nil || recipientID == "" || claims.UserID != recipientID {
				response.Error(w, r, http.StatusForbidden, "FORBIDDEN", "Access to this notification inbox is forbidden", nil)
				return
			}
			next(w, r)
		}))
	}

	mux.Handle("GET /api/v1/notifications/{recipientId}", requireRecipientOwner(func(w http.ResponseWriter, r *http.Request) {
		recipientID := r.PathValue("recipientId")
		items := dispatcher.ListInbox(recipientID)
		unread := dispatcher.UnreadCount(recipientID)
		response.JSON(w, http.StatusOK, "Notifications retrieved", map[string]any{
			"items":       items,
			"unreadCount": unread,
		}, &response.Metadata{
			Total:     int64(len(items)),
			Page:      1,
			PageCount: 1,
			Limit:     50,
		})
	}))

	mux.Handle("PATCH /api/v1/notifications/{recipientId}/{notificationId}/read", requireRecipientOwner(func(w http.ResponseWriter, r *http.Request) {
		recipientID := r.PathValue("recipientId")
		notificationID := r.PathValue("notificationId")
		found := dispatcher.MarkAsRead(recipientID, notificationID)
		if !found {
			response.Error(w, r, http.StatusNotFound, "NOT_FOUND", "Notification not found", nil)
			return
		}
		response.JSON(w, http.StatusOK, "Notification marked as read", map[string]any{
			"notificationId": notificationID,
			"isRead":         true,
			"unreadCount":    dispatcher.UnreadCount(recipientID),
		}, nil)
	}))

	mux.Handle("PATCH /api/v1/notifications/{recipientId}/read-all", requireRecipientOwner(func(w http.ResponseWriter, r *http.Request) {
		recipientID := r.PathValue("recipientId")
		count := dispatcher.MarkAllAsRead(recipientID)
		response.JSON(w, http.StatusOK, "All notifications marked as read", map[string]any{
			"updatedCount": count,
			"unreadCount":  0,
		}, nil)
	}))

	mux.Handle("POST /api/v1/notifications/dispatch", internalauth.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req domain.NotificationItem
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.EventID == "" || req.RecipientID == "" {
			response.Error(w, r, http.StatusBadRequest, "INVALID_PAYLOAD", "eventId and recipientId are required", nil)
			return
		}
		if req.Channel == "" {
			req.Channel = "IN_APP"
		}
		item, deduplicated := dispatcher.Dispatch(req)
		msg := "Notification dispatched"
		if deduplicated {
			msg = "Duplicate notification ignored (idempotent delivery)"
		}
		response.JSON(w, http.StatusOK, msg, map[string]any{
			"notification": item,
			"deduplicated": deduplicated,
		}, nil)
	})))

	mux.Handle("GET /api/v1/notifications/{recipientId}/stream", requireRecipientOwner(func(w http.ResponseWriter, r *http.Request) {
		recipientID := r.PathValue("recipientId")
		flusher, ok := w.(http.Flusher)
		if !ok {
			response.Error(w, r, http.StatusInternalServerError, "STREAMING_UNSUPPORTED", "Streaming is not supported", nil)
			return
		}
		claims, _ := auth.ClaimsFrom(r.Context())
		lifetime := untilExpiry(claims, time.Now())
		if lifetime <= 0 {
			response.Error(w, r, http.StatusUnauthorized, "INVALID_ACCESS_TOKEN", "The access token is invalid or expired", nil)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)

		fmt.Fprintf(w, "event: connected\ndata: {\"recipientId\":%q,\"timestamp\":%q}\n\n", recipientID, time.Now().UTC().Format(time.RFC3339))
		flusher.Flush()

		ch, unsub := dispatcher.Subscribe(recipientID)
		defer unsub()

		expiry := time.NewTimer(lifetime)
		defer expiry.Stop()
		tick := time.NewTicker(keepAlive)
		defer tick.Stop()

		for {
			select {
			case <-r.Context().Done():
				return
			case <-expiry.C:
				return
			case noti, open := <-ch:
				if !open {
					return
				}
				data, err := json.Marshal(noti)
				if err == nil {
					fmt.Fprintf(w, "event: notification\ndata: %s\n\n", data)
					flusher.Flush()
				}
			case <-tick.C:
				fmt.Fprint(w, ": keep-alive\n\n")
				flusher.Flush()
			}
		}
	}))

	return middleware.Chain(mux)
}

func main() {
	port := config.EnvOrDefault("NOTIFICATION_PORT", "8089")
	pgURL := config.EnvOrDefault("DATABASE_URL_NOTIFICATIONS", "postgres://notify_svc_user:notify_secret@localhost:5432/commerce_lab?sslmode=disable&search_path=notifications")

	secrets := config.NewLoader()
	auth.LoadSecrets(secrets)
	internalauth.LoadToken(secrets)
	if err := secrets.Err(); err != nil {
		log.Fatalf("[notification-service] %v", err)
	}

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

	// Seed sample notifications for development users.
	dispatcher.Dispatch(domain.NotificationItem{
		NotificationID: "NOTI-INIT-01",
		EventID:        "evt-seed-01",
		RecipientID:    "user-lan",
		Channel:        "IN_APP",
		Category:       "ORDER_CONFIRMED",
		Title:          "Order O-001 confirmed",
		Message:        "Warehouse WH-HCM-01 is packing your order items.",
	})
	dispatcher.Dispatch(domain.NotificationItem{
		NotificationID: "NOTI-INIT-02",
		EventID:        "evt-seed-02",
		RecipientID:    "warehouse-minh",
		Channel:        "IN_APP",
		Category:       "WAREHOUSE_STOP_SIGNAL",
		Title:          "Alert: Stop packing request for S-001",
		Message:        "Customer requested cancellation of order O-001. Confirm stop before courier handover (SC06).",
	})

	log.Printf("[notification-service] listening on :%s (schema: notifications, mongo: notify_user_inboxes)", port)
	log.Fatal(http.ListenAndServe(":"+port, NewServer(dispatcher, hub, defaultKeepAlive)))
}
