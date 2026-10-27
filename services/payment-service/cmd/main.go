package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/contracts/events"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/config"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/db"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/eventbus"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/middleware"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
	"github.com/hieuvu0611/commercelab-retail-be/services/payment-service/internal/domain"
	"github.com/hieuvu0611/commercelab-retail-be/services/payment-service/internal/repository"
	"github.com/jackc/pgx/v5/pgxpool"
)

func NewServer(repo *repository.PaymentRepository, bus *eventbus.Bus, pgConnected, mongoConnected bool) http.Handler {
	if bus == nil {
		bus = eventbus.New()
	}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		response.JSON(w, http.StatusOK, "payment-service healthy", map[string]any{
			"service":        "payment-service",
			"postgresSchema": "payments",
			"mongoColl":      "payment_raw_webhooks (TTL 90d)",
			"pgConnected":    pgConnected,
			"mongoConnected": mongoConnected,
		}, nil)
	})

	// B03, P05, SC05: Create a payment attempt (blocks duplicate active attempts)
	mux.HandleFunc("POST /api/v1/payments/attempts", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			AttemptID      string `json:"attemptId"`
			OrderID        string `json:"orderId"`
			IdempotencyKey string `json:"idempotencyKey"`
			AmountVND      int64  `json:"amountVnd"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			response.Error(w, r, http.StatusBadRequest, "INVALID_PAYLOAD", "Invalid payment attempt request payload", nil)
			return
		}
		if req.IdempotencyKey == "" {
			req.IdempotencyKey = r.Header.Get("Idempotency-Key")
		}
		att, err := repo.CreateAttempt(r.Context(), req.AttemptID, req.OrderID, req.IdempotencyKey, req.AmountVND)
		if err != nil {
			if errors.Is(err, domain.ErrActivePaymentInProgress) {
				response.Error(w, r, http.StatusConflict, "ACTIVE_PAYMENT_IN_PROGRESS", err.Error(), nil)
				return
			}
			response.Error(w, r, http.StatusBadRequest, "PAYMENT_ATTEMPT_REJECTED", err.Error(), nil)
			return
		}
		response.JSON(w, http.StatusCreated, "Payment attempt initiated (B03, SC05)", att, nil)
	})

	// B03, P05, SC05: Process payment gateway webhook (CAPTURED, FAILED, UNKNOWN, EXPIRE_30M)
	mux.HandleFunc("POST /api/v1/payments/webhooks", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			OrderID    string         `json:"orderId"`
			AttemptID  string         `json:"attemptId"`
			Outcome    string         `json:"outcome"`
			AmountVND  int64          `json:"amountVnd"`
			RawPayload map[string]any `json:"rawPayload"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.OrderID == "" || req.Outcome == "" {
			response.Error(w, r, http.StatusBadRequest, "INVALID_PAYLOAD", "orderId and outcome are required", nil)
			return
		}

		sum, att, autoRefundRequired, err := repo.ProcessWebhook(
			r.Context(),
			req.OrderID,
			req.AttemptID,
			req.Outcome,
			req.AmountVND,
			req.RawPayload,
		)
		if err != nil {
			response.Error(w, r, http.StatusBadRequest, "WEBHOOK_PROCESSING_FAILED", err.Error(), nil)
			return
		}

		switch {
		case autoRefundRequired:
			_ = bus.Publish(r.Context(), "EVT-LATE-REF-"+req.OrderID, events.EventPaymentLateRefundNeed, "Payment", req.OrderID, map[string]any{
				"orderId":            req.OrderID,
				"attemptId":          att.AttemptID,
				"capturedAmountVnd":  sum.CapturedAmount,
				"autoRefundRequired": true,
			})
		case req.Outcome == "CAPTURED":
			_ = bus.Publish(r.Context(), "EVT-PAY-CAP-"+req.OrderID, events.EventPaymentCaptured, "Payment", req.OrderID, map[string]any{
				"orderId":           req.OrderID,
				"attemptId":         att.AttemptID,
				"capturedAmountVnd": sum.CapturedAmount,
			})
		case req.Outcome == "FAILED":
			_ = bus.Publish(r.Context(), "EVT-PAY-FAIL-"+req.OrderID, events.EventPaymentFailed, "Payment", req.OrderID, map[string]any{
				"orderId":   req.OrderID,
				"attemptId": att.AttemptID,
			})
		case req.Outcome == "UNKNOWN":
			_ = bus.Publish(r.Context(), "EVT-PAY-UNK-"+req.OrderID, events.EventPaymentUnknown30m, "Payment", req.OrderID, map[string]any{
				"orderId":         req.OrderID,
				"attemptId":       att.AttemptID,
				"unknownDeadline": att.UnknownDeadline,
			})
		}

		response.JSON(w, http.StatusOK, "Payment webhook processed (B03, SC05)", map[string]any{
			"summary":             sum,
			"remainingRefundable": sum.RemainingRefundable(),
			"attempt":             att,
			"autoRefundRequired":  autoRefundRequired,
		}, nil)
	})

	// Query order payment summary & latest attempt
	mux.HandleFunc("GET /api/v1/payments/{orderId}", func(w http.ResponseWriter, r *http.Request) {
		orderID := r.PathValue("orderId")
		sum, att, err := repo.GetOrderPayment(r.Context(), orderID)
		if err != nil {
			response.Error(w, r, http.StatusNotFound, "PAYMENT_NOT_FOUND", err.Error(), nil)
			return
		}
		response.JSON(w, http.StatusOK, "Order payment summary retrieved", map[string]any{
			"summary":             sum,
			"remainingRefundable": sum.RemainingRefundable(),
			"attempt":             att,
		}, nil)
	})

	// I03, SC08: Reserve refund ceiling before gateway payout
	mux.HandleFunc("POST /api/v1/payments/{orderId}/refund-reservations", func(w http.ResponseWriter, r *http.Request) {
		orderID := r.PathValue("orderId")
		var req struct {
			AmountVND int64 `json:"amountVnd"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			response.Error(w, r, http.StatusBadRequest, "INVALID_PAYLOAD", "Invalid refund reservation payload", nil)
			return
		}
		summary, err := repo.ReserveRefundCeiling(r.Context(), orderID, req.AmountVND)
		if err != nil {
			response.Error(w, r, http.StatusConflict, "REFUND_CEILING_EXCEEDED", err.Error(), nil)
			return
		}
		response.JSON(w, http.StatusCreated, "Refund ceiling reserved (I03, SC08)", summary, nil)
	})

	// I03, SC08: Complete a pending refund after gateway confirmation
	mux.HandleFunc("POST /api/v1/payments/{orderId}/refund-completions", func(w http.ResponseWriter, r *http.Request) {
		orderID := r.PathValue("orderId")
		var req struct {
			AmountVND int64 `json:"amountVnd"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			response.Error(w, r, http.StatusBadRequest, "INVALID_PAYLOAD", "Invalid refund completion payload", nil)
			return
		}
		summary, err := repo.CompleteRefund(r.Context(), orderID, req.AmountVND)
		if err != nil {
			response.Error(w, r, http.StatusConflict, "INVALID_PENDING_REFUND", err.Error(), nil)
			return
		}
		response.JSON(w, http.StatusOK, "Refund completion recorded (I03, SC08)", summary, nil)
	})

	return middleware.Chain(mux)
}

func main() {
	port := config.EnvOrDefault("PAYMENT_PORT", "8084")
	engine := domain.NewPaymentEngine()

	var pgPool *pgxpool.Pool
	pgConnected := false
	if dsn := os.Getenv("POSTGRES_DSN"); dsn != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		pool, err := db.ConnectPostgres(ctx, dsn)
		cancel()
		if err != nil {
			log.Printf("[payment-service] postgres unavailable (%v), using in-memory engine", err)
		} else {
			pgPool = pool
			pgConnected = true
			defer pgPool.Close()
		}
	}

	var mongoStore *db.MongoStore
	mongoConnected := false
	if mongoURI := os.Getenv("MONGO_URI"); mongoURI != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		store, _, err := db.ConnectMongo(ctx, mongoURI, config.EnvOrDefault("MONGO_DB", "commerce_lab_doc"))
		cancel()
		if err == nil {
			mongoStore = store
			mongoConnected = true
		}
	}

	repo := repository.NewPaymentRepository(pgPool, mongoStore, engine)
	bus := eventbus.New()
	log.Printf("[payment-service] listening on :%s (schema: payments, mongo: payment_raw_webhooks, pg=%v, mongo=%v)", port, pgConnected, mongoConnected)
	log.Fatal(http.ListenAndServe(":"+port, NewServer(repo, bus, pgConnected, mongoConnected)))
}
