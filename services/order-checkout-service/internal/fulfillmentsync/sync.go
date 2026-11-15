// Package fulfillmentsync applies fulfillment events to orders: a shipment moves a confirmed order into
// fulfillment and its delivery completes it (B04). Every step is a compare-and-set, so repeated or late events change nothing.
package fulfillmentsync

import (
	"context"
	"errors"
	"log"

	"github.com/hieuvu0611/commercelab-retail-be/contracts/events"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/orders"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/saga"
)

// CancelSettler applies the warehouse's answer to a waiting cancellation (SC06): a stopped parcel cancels the
// order, a handed over one rejects the cancellation. It is idempotent and only returns retryable errors.
type CancelSettler interface {
	SettleCancel(ctx context.Context, orderID string) error
}

// Handler handles shipment events. Cancels is optional: without it stop and handover events are ignored.
type Handler struct {
	Orders  orders.Store
	Cancels CancelSettler
}

// Handle implements eventconsumer.Handler. Store failures are returned (the event is retried); an event that
// names an unknown order or an order in an unrelated state is logged and skipped.
func (h *Handler) Handle(ctx context.Context, e events.DomainEvent) error {
	switch e.EventType {
	case events.EventShipmentCreated:
		return h.move(ctx, e, saga.StateConfirmed, saga.StateInFulfillment)
	case events.EventShipmentDelivered:
		return h.move(ctx, e, saga.StateInFulfillment, saga.StateDelivered)
	case events.EventShipmentStopped, events.EventShipmentHandedOver:
		return h.settle(ctx, e)
	}
	return nil
}

// settle lets a stopped or handed over parcel decide a cancellation that waits for the warehouse.
func (h *Handler) settle(ctx context.Context, e events.DomainEvent) error {
	if h.Cancels == nil {
		return nil
	}
	orderID, _ := e.Payload["orderId"].(string)
	if orderID == "" {
		log.Printf("[order] %s without an order id; skipped", e.EventType)
		return nil
	}
	return h.Cancels.SettleCancel(ctx, orderID)
}

// move changes the order from one state to the next; being there already (or beyond) is success.
func (h *Handler) move(ctx context.Context, e events.DomainEvent, from, to saga.State) error {
	orderID, _ := e.Payload["orderId"].(string)
	o, err := h.Orders.Get(ctx, orderID)
	if errors.Is(err, orders.ErrNotFound) {
		log.Printf("[order] %s names unknown order %q; skipped", e.EventType, orderID)
		return nil
	}
	if err != nil {
		return err
	}
	if o.Status == string(to) || o.Status == string(saga.StateDelivered) {
		return nil
	}
	if o.Status != string(from) {
		log.Printf("[order] %s for order %s in state %s ignored", e.EventType, orderID, o.Status)
		return nil
	}
	_, err = h.Orders.Transition(ctx, orderID, string(from), string(to), o.PaymentStatus, o.FailureCode)
	if errors.Is(err, orders.ErrStaleState) {
		return h.move(ctx, e, from, to) // lost a race: look again
	}
	return err
}
