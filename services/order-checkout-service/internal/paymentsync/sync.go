// Package paymentsync applies payment events to orders: a payout moves an order's money from "owed back" to
// "refunded". The event carries absolute totals, so repeated or reordered deliveries change nothing.
package paymentsync

import (
	"context"
	"log"

	"github.com/hieuvu0611/commercelab-retail-be/contracts/events"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/checkout"
)

// RefundApplier stores a payout on the order (implemented by checkout.Service).
type RefundApplier interface {
	ApplyRefund(ctx context.Context, orderID string, r checkout.RefundReport) error
}

// Handler handles payment events.
type Handler struct{ Refunds RefundApplier }

// Handle implements eventconsumer.Handler. Retryable (dependency) errors are returned so the event is tried
// again; anything that can never succeed is logged and skipped so it cannot block the stream.
func (h *Handler) Handle(ctx context.Context, e events.DomainEvent) error {
	if e.EventType != events.EventPaymentRefunded {
		return nil
	}
	orderID, _ := e.Payload["orderId"].(string)
	refunded, okRefunded := number(e.Payload["refundedTotalVnd"])
	pending, okPending := number(e.Payload["refundPendingVnd"])
	captured, okCaptured := number(e.Payload["capturedVnd"])
	if orderID == "" || !okRefunded || !okPending || !okCaptured {
		log.Printf("[order] skipping %s of order %q: unreadable payload", e.EventType, orderID)
		return nil
	}
	err := h.Refunds.ApplyRefund(ctx, orderID, checkout.RefundReport{RefundedVND: refunded, PendingVND: pending, CapturedVND: captured})
	if err != nil && !dependency.IsUnavailable(err) {
		log.Printf("[order] refund event for order %s could not be applied: %v", orderID, err)
		return nil
	}
	return err
}

// number reads a whole VND amount from a decoded JSON number.
func number(v any) (int64, bool) {
	f, ok := v.(float64)
	if !ok || f != float64(int64(f)) {
		return 0, false
	}
	return int64(f), true
}
