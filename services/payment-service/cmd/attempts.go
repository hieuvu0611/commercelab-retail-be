package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/contracts/ids"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/internalauth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/request"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
	"github.com/hieuvu0611/commercelab-retail-be/services/payment-service/internal/attempt"
)

const (
	maxIDLength      = 64
	maxWebhookBytes  = 64 << 10
	signatureHeader  = "X-Signature"
	maxEventIDLength = 128
	maxCloseBytes    = 1 << 10
)

var idempotencyKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// Options configure the payment HTTP API.
type Options struct {
	// SimulatorEnabled turns the customer-facing outcome simulator on (development only).
	SimulatorEnabled bool
	// WebhookSecret signs webhook bodies (HMAC-SHA256, hex, X-Signature header).
	WebhookSecret string
	// Orders is told about every outcome so the order saga can confirm or close the order; nil disables it.
	Orders OrderNotifier
}

// OrderNotifier reports the state of an attempt to the order service. It must be idempotent on the receiving side.
type OrderNotifier interface {
	PaymentOutcome(ctx context.Context, a attempt.Attempt) error
}

// notifyTimeout bounds the callback to the order service; it outlives a client that hangs up.
const notifyTimeout = 10 * time.Second

func registerAttemptRoutes(mux *http.ServeMux, store attempt.Store, opts Options) {
	api := &attemptAPI{store: store, opts: opts}
	mux.Handle("POST /api/v1/payments/attempts", internalauth.Require(http.HandlerFunc(api.create)))
	mux.Handle("GET /api/v1/payments/attempts/{attemptId}", auth.RequireAuth(http.HandlerFunc(api.get)))
	mux.Handle("POST /api/v1/payments/simulator/{attemptId}/outcome", auth.RequireAuth(http.HandlerFunc(api.simulate)))
	mux.Handle("GET /api/v1/payments/orders/{orderId}/attempt", internalauth.Require(http.HandlerFunc(api.byOrder)))
	mux.Handle("POST /api/v1/payments/attempts/{attemptId}/close", internalauth.Require(http.HandlerFunc(api.close)))
	mux.HandleFunc("POST /api/v1/payments/webhooks/simulator", api.webhook)
}

type attemptAPI struct {
	store attempt.Store
	opts  Options
}

func (a *attemptAPI) create(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("Idempotency-Key")
	if !idempotencyKeyPattern.MatchString(key) {
		response.Error(w, r, http.StatusBadRequest, "MISSING_IDEMPOTENCY_KEY", "A valid Idempotency-Key header is required", nil)
		return
	}
	var body struct {
		OrderID   string `json:"orderId"`
		UserID    string `json:"userId"`
		AmountVND int64  `json:"amountVnd"`
	}
	if !request.DecodeJSON(w, r, &body) {
		return
	}
	body.OrderID, body.UserID = strings.TrimSpace(body.OrderID), strings.TrimSpace(body.UserID)
	problems := map[string][]string{}
	if body.OrderID == "" || len(body.OrderID) > maxIDLength {
		problems["orderId"] = []string{"orderId is required"}
	}
	if body.UserID == "" || len(body.UserID) > maxIDLength {
		problems["userId"] = []string{"userId is required"}
	}
	if body.AmountVND <= 0 {
		problems["amountVnd"] = []string{"amountVnd must be a positive whole number of VND"}
	}
	if len(problems) > 0 {
		response.Error(w, r, http.StatusBadRequest, "VALIDATION_FAILED", "The request has invalid fields", problems)
		return
	}
	stored, replayed, err := a.store.Create(r.Context(), attempt.Attempt{
		AttemptID: ids.PaymentAttemptID(body.OrderID), OrderID: body.OrderID, UserID: body.UserID, IdempotencyKey: key, AmountVND: body.AmountVND,
	})
	switch {
	case errors.Is(err, attempt.ErrIdempotencyConflict):
		response.Error(w, r, http.StatusConflict, "IDEMPOTENCY_KEY_REUSED", "This Idempotency-Key was already used for a different payment", nil)
	case errors.Is(err, attempt.ErrActivePayment):
		response.Error(w, r, http.StatusConflict, "PAYMENT_IN_PROGRESS", "A payment is already in progress for this order", nil)
	case errors.Is(err, attempt.ErrAttemptExists):
		response.Error(w, r, http.StatusConflict, "PAYMENT_ATTEMPT_EXISTS", "This order already has a payment attempt", nil)
	case err != nil:
		writeStoreError(w, r, err)
	case replayed:
		response.JSON(w, http.StatusOK, "Payment attempt already created", stored, nil)
	default:
		response.JSON(w, http.StatusCreated, "Payment attempt created", stored, nil)
	}
}

// own loads an attempt the caller owns; another customer's attempt looks missing.
func (a *attemptAPI) own(w http.ResponseWriter, r *http.Request) (attempt.Attempt, bool) {
	claims, _ := auth.ClaimsFrom(r.Context())
	cur, err := a.store.Get(r.Context(), r.PathValue("attemptId"))
	if errors.Is(err, attempt.ErrNotFound) || (err == nil && cur.UserID != claims.UserID) {
		response.Error(w, r, http.StatusNotFound, "PAYMENT_NOT_FOUND", "Payment attempt not found", nil)
		return attempt.Attempt{}, false
	}
	if err != nil {
		writeStoreError(w, r, err)
		return attempt.Attempt{}, false
	}
	return cur, true
}

func (a *attemptAPI) get(w http.ResponseWriter, r *http.Request) {
	if cur, ok := a.own(w, r); ok {
		response.JSON(w, http.StatusOK, "Payment attempt", cur, nil)
	}
}

type outcomeBody struct {
	Outcome   string `json:"outcome"`
	AmountVND *int64 `json:"amountVnd"`
}

func validOutcome(o string) bool {
	switch attempt.Outcome(o) {
	case attempt.OutcomeCaptured, attempt.OutcomeFailed, attempt.OutcomeUnknown:
		return true
	}
	return false
}

// simulate lets the customer who owns an attempt report how the simulated payment went. It exists for
// development and demos only and answers 404 unless PAYMENT_SIMULATOR_ENABLED allows it.
func (a *attemptAPI) simulate(w http.ResponseWriter, r *http.Request) {
	if !a.opts.SimulatorEnabled {
		response.Error(w, r, http.StatusNotFound, "NOT_FOUND", "Not found", nil)
		return
	}
	cur, ok := a.own(w, r)
	if !ok {
		return
	}
	var body outcomeBody
	if !request.DecodeJSON(w, r, &body) {
		return
	}
	if !validOutcome(body.Outcome) {
		response.Error(w, r, http.StatusBadRequest, "VALIDATION_FAILED", "The request has invalid fields",
			map[string][]string{"outcome": {"outcome must be CAPTURED, FAILED or UNKNOWN"}})
		return
	}
	amount := cur.AmountVND
	if body.AmountVND != nil {
		amount = *body.AmountVND
	}
	a.apply(w, r, cur.AttemptID, attempt.Outcome(body.Outcome), amount, "")
}

// webhook receives the provider's signed outcome notification. The signature is checked before anything in the
// body is read, and an event id that was already applied changes nothing.
func (a *attemptAPI) webhook(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBytes))
	if err != nil {
		response.Error(w, r, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "Request body is too large", nil)
		return
	}
	if !validSignature(a.opts.WebhookSecret, raw, r.Header.Get(signatureHeader)) {
		response.Error(w, r, http.StatusUnauthorized, "INVALID_SIGNATURE", "The webhook signature is missing or invalid", nil)
		return
	}
	var body struct {
		EventID   string `json:"eventId"`
		AttemptID string `json:"attemptId"`
		Outcome   string `json:"outcome"`
		AmountVND int64  `json:"amountVnd"`
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || body.EventID == "" || len(body.EventID) > maxEventIDLength ||
		body.AttemptID == "" || len(body.AttemptID) > maxIDLength || !validOutcome(body.Outcome) {
		response.Error(w, r, http.StatusBadRequest, "VALIDATION_FAILED", "The webhook payload is invalid", nil)
		return
	}
	a.apply(w, r, body.AttemptID, attempt.Outcome(body.Outcome), body.AmountVND, body.EventID)
}

func (a *attemptAPI) apply(w http.ResponseWriter, r *http.Request, attemptID string, o attempt.Outcome, amount int64, eventID string) {
	updated, change, err := a.store.Apply(r.Context(), attemptID, o, amount, eventID)
	switch {
	case errors.Is(err, attempt.ErrNotFound):
		response.Error(w, r, http.StatusNotFound, "PAYMENT_NOT_FOUND", "Payment attempt not found", nil)
	case errors.Is(err, attempt.ErrFinalOutcome):
		response.Error(w, r, http.StatusConflict, "PAYMENT_ALREADY_FINAL", "The payment already has a different final outcome", nil)
	case errors.Is(err, attempt.ErrInvalidOutcome):
		response.Error(w, r, http.StatusBadRequest, "VALIDATION_FAILED", "The outcome or amount is invalid", nil)
	case err != nil:
		writeStoreError(w, r, err)
	case change.Replay:
		a.notifyOrder(r, updated)
		response.JSON(w, http.StatusOK, "Outcome already applied", updated, nil)
	default:
		a.notifyOrder(r, updated)
		response.JSON(w, http.StatusOK, "Outcome recorded", updated, nil)
	}
}

// notifyOrder reports the stored attempt to the order service. A failure is logged and does not fail the
// request: the outcome is recorded, the provider redelivers, and the order service reconciles on its own.
func (a *attemptAPI) notifyOrder(r *http.Request, at attempt.Attempt) {
	if a.opts.Orders == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), notifyTimeout)
	defer cancel()
	if err := a.opts.Orders.PaymentOutcome(ctx, at); err != nil {
		log.Printf("[payment] order callback for attempt %s failed (it is retried on redelivery or by reconcile): %v", at.AttemptID, err)
	}
}

// byOrder lets the order service read where an order's payment stands (it reconciles lost callbacks with it).
func (a *attemptAPI) byOrder(w http.ResponseWriter, r *http.Request) {
	cur, err := a.store.GetByOrder(r.Context(), r.PathValue("orderId"))
	switch {
	case errors.Is(err, attempt.ErrNotFound):
		response.Error(w, r, http.StatusNotFound, "PAYMENT_NOT_FOUND", "Payment attempt not found", nil)
	case err != nil:
		writeStoreError(w, r, err)
	default:
		response.JSON(w, http.StatusOK, "Payment attempt", cur, nil)
	}
}

// closeReason reads the optional body of a close: {"reason":"ORDER_CANCELLED"} says money already captured is owed
// back because the customer cancelled; no body keeps the default (a late or lost order).
func closeReason(w http.ResponseWriter, r *http.Request) (string, bool) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxCloseBytes))
	if err != nil {
		response.Error(w, r, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "Request body is too large", nil)
		return "", false
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return attempt.ReasonLateCapture, true
	}
	var body struct {
		Reason string `json:"reason"`
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || (body.Reason != "" && body.Reason != attempt.ReasonOrderCancelled) {
		response.Error(w, r, http.StatusBadRequest, "VALIDATION_FAILED", "The request has invalid fields",
			map[string][]string{"reason": {"reason must be ORDER_CANCELLED"}})
		return "", false
	}
	if body.Reason == "" {
		return attempt.ReasonLateCapture, true
	}
	return body.Reason, true
}

// close ends an attempt because the order service closed its order. The order service already knows, so
// nothing is reported back.
func (a *attemptAPI) close(w http.ResponseWriter, r *http.Request) {
	reason, ok := closeReason(w, r)
	if !ok {
		return
	}
	closed, _, err := a.store.CloseFor(r.Context(), r.PathValue("attemptId"), reason)
	switch {
	case errors.Is(err, attempt.ErrNotFound):
		response.Error(w, r, http.StatusNotFound, "PAYMENT_NOT_FOUND", "Payment attempt not found", nil)
	case err != nil:
		writeStoreError(w, r, err)
	default:
		response.JSON(w, http.StatusOK, "Payment attempt closed", closed, nil)
	}
}

// validSignature checks hex(HMAC-SHA256(secret, body)) in constant time. An empty secret never validates.
func validSignature(secret string, body []byte, got string) bool {
	if secret == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(strings.ToLower(got)), []byte(want))
}

func writeStoreError(w http.ResponseWriter, r *http.Request, err error) {
	if dependency.IsUnavailable(err) {
		dependency.WriteUnavailable(w, r, err)
		return
	}
	log.Printf("[payment] request failed: %v", err)
	response.Error(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal server error", nil)
}
