// Package codsync consumes shipment COD events (payment.cod_collected.v1 / payment.cod_cancelled.v1)
// and synchronizes payment attempts and order payment status idempotently (BE-T12).
package codsync

import (
	"context"
	"errors"
	"log"

	"github.com/hieuvu0611/commercelab-retail-be/contracts/events"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/services/payment-service/internal/attempt"
)

// OrderNotifier reports attempt outcomes to order-checkout-service.
type OrderNotifier interface {
	PaymentOutcome(ctx context.Context, a attempt.Attempt) error
}

// Handler consumes fulfillment COD events.
type Handler struct {
	Store  attempt.Store
	Orders OrderNotifier
}

func (h *Handler) Handle(ctx context.Context, e events.DomainEvent) error {
	switch e.EventType {
	case events.EventPaymentCODCollected:
		return h.applyCOD(ctx, e, attempt.OutcomeCODCollected)
	case events.EventPaymentCODCancelled:
		if cancelOrder, hasFlag := e.Payload["cancelOrderPayment"].(bool); hasFlag && !cancelOrder {
			return nil // Partial-parcel failure on multi-parcel order; remaining parcel cash still pending
		}
		return h.applyCOD(ctx, e, attempt.OutcomeCODCancelled)
	default:
		return nil
	}
}

func (h *Handler) applyCOD(ctx context.Context, e events.DomainEvent, outcome attempt.Outcome) error {
	orderID, _ := e.Payload["orderId"].(string)
	if orderID == "" {
		log.Printf("[payment] skipping %s: missing orderId", e.EventType)
		return nil
	}
	cur, err := h.Store.GetByOrder(ctx, orderID)
	if errors.Is(err, attempt.ErrNotFound) {
		log.Printf("[payment] skipping %s for unknown order %s", e.EventType, orderID)
		return nil
	}
	if err != nil {
		if dependency.IsUnavailable(err) {
			return err
		}
		log.Printf("[payment] lookup attempt for order %s failed: %v", orderID, err)
		return nil
	}

	var amount int64
	if outcome == attempt.OutcomeCODCollected {
		if rawAmt, ok := e.Payload["amountVnd"].(float64); ok && rawAmt > 0 {
			amount = int64(rawAmt)
		} else {
			amount = cur.AmountVND
		}
	}

	updated, _, err := h.Store.Apply(ctx, cur.AttemptID, outcome, amount, e.EventID)
	if err != nil {
		if dependency.IsUnavailable(err) {
			return err
		}
		log.Printf("[payment] applying %s to attempt %s failed: %v", outcome, cur.AttemptID, err)
		return nil
	}

	if h.Orders != nil {
		if err := h.Orders.PaymentOutcome(ctx, updated); err != nil {
			if dependency.IsUnavailable(err) {
				return err
			}
			log.Printf("[payment] notify order %s after COD %s failed: %v", orderID, outcome, err)
		}
	}
	return nil
}
