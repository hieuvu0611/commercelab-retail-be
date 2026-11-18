package codsync

import (
	"context"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/contracts/events"
	"github.com/hieuvu0611/commercelab-retail-be/services/payment-service/internal/attempt"
)

type spyOrders struct {
	calls []attempt.Attempt
}

func (s *spyOrders) PaymentOutcome(_ context.Context, a attempt.Attempt) error {
	s.calls = append(s.calls, a)
	return nil
}

func TestHandleCODCollectedAndCancelledEvents(t *testing.T) {
	ctx := context.Background()
	store := attempt.NewMemoryStore()
	_, _, err := store.Create(ctx, attempt.Attempt{
		AttemptID:      "PAY-O-COD-1",
		OrderID:        "O-COD-1",
		UserID:         "user-lan",
		IdempotencyKey: "k-1",
		AmountVND:      500000,
		PaymentMethod:  "COD",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = store.Create(ctx, attempt.Attempt{
		AttemptID:      "PAY-O-COD-2",
		OrderID:        "O-COD-2",
		UserID:         "user-lan",
		IdempotencyKey: "k-2",
		AmountVND:      400000,
		PaymentMethod:  "COD",
	})
	if err != nil {
		t.Fatal(err)
	}

	orders := &spyOrders{}
	h := &Handler{Store: store, Orders: orders}

	// Partial parcel collect followed by second parcel collect completes COD attempt
	if err := h.Handle(ctx, events.DomainEvent{
		EventID:   "evt-c1",
		EventType: events.EventPaymentCODCollected,
		Payload:   map[string]any{"orderId": "O-COD-1", "amountVnd": float64(300000)},
	}); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Get(ctx, "PAY-O-COD-1")
	if got.Status != attempt.StatusCODPending || got.CapturedAmountVND != 300000 {
		t.Fatalf("after partial COD collect = %+v", got)
	}
	if err := h.Handle(ctx, events.DomainEvent{
		EventID:   "evt-c2",
		EventType: events.EventPaymentCODCollected,
		Payload:   map[string]any{"orderId": "O-COD-1", "amountVnd": float64(200000)},
	}); err != nil {
		t.Fatal(err)
	}
	got, _ = store.Get(ctx, "PAY-O-COD-1")
	if got.Status != attempt.StatusCOLLECTED || got.CapturedAmountVND != 500000 {
		t.Fatalf("after full COD collect = %+v", got)
	}

	// Partial parcel delivery failure with cancelOrderPayment=false leaves order attempt pending
	if err := h.Handle(ctx, events.DomainEvent{
		EventID:   "evt-f-partial",
		EventType: events.EventPaymentCODCancelled,
		Payload:   map[string]any{"orderId": "O-COD-2", "cancelOrderPayment": false},
	}); err != nil {
		t.Fatal(err)
	}
	got2, _ := store.Get(ctx, "PAY-O-COD-2")
	if got2.Status != attempt.StatusCODPending {
		t.Fatalf("expected COD_PENDING after partial parcel fail, got %s", got2.Status)
	}

	// Full failure cancels order attempt
	if err := h.Handle(ctx, events.DomainEvent{
		EventID:   "evt-f-all",
		EventType: events.EventPaymentCODCancelled,
		Payload:   map[string]any{"orderId": "O-COD-2", "cancelOrderPayment": true},
	}); err != nil {
		t.Fatal(err)
	}
	got2, _ = store.Get(ctx, "PAY-O-COD-2")
	if got2.Status != attempt.StatusCODCancelled {
		t.Fatalf("expected COD_CANCELLED, got %s", got2.Status)
	}
	if len(orders.calls) != 3 {
		t.Fatalf("expected 3 order notifications, got %d", len(orders.calls))
	}
}
