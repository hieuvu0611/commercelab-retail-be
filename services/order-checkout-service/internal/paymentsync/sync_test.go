package paymentsync

import (
	"context"
	"errors"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/contracts/events"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/checkout"
)

type fakeRefunds struct {
	got []checkout.RefundReport
	ids []string
	err error
}

func (f *fakeRefunds) ApplyRefund(_ context.Context, orderID string, r checkout.RefundReport) error {
	f.ids, f.got = append(f.ids, orderID), append(f.got, r)
	return f.err
}

func refunded(payload map[string]any) events.DomainEvent {
	return events.DomainEvent{EventID: "evt-1", EventType: events.EventPaymentRefunded, AggregateType: "Payment", AggregateID: "O-1", Payload: payload}
}

func TestRefundedEventsAreAppliedWithTheirAbsoluteTotals(t *testing.T) {
	f := &fakeRefunds{}
	h := &Handler{Refunds: f}
	// JSON numbers arrive as float64.
	e := refunded(map[string]any{"orderId": "O-1", "refundedTotalVnd": float64(660000), "refundPendingVnd": float64(0), "capturedVnd": float64(660000)})
	if err := h.Handle(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if len(f.got) != 1 || f.ids[0] != "O-1" || f.got[0] != (checkout.RefundReport{RefundedVND: 660000, PendingVND: 0, CapturedVND: 660000}) {
		t.Fatalf("applied = %v %v", f.ids, f.got)
	}
}

func TestOtherEventsAndBrokenPayloadsAreSkippedAndOutagesRetried(t *testing.T) {
	f := &fakeRefunds{}
	h := &Handler{Refunds: f}
	other := refunded(map[string]any{"orderId": "O-1"})
	other.EventType = events.EventPaymentCaptured
	if err := h.Handle(context.Background(), other); err != nil || len(f.got) != 0 {
		t.Fatalf("other event: %v %v", err, f.got)
	}
	if err := h.Handle(context.Background(), refunded(map[string]any{"refundedTotalVnd": float64(1)})); err != nil || len(f.got) != 0 {
		t.Fatalf("no order id: %v %v", err, f.got)
	}
	if err := h.Handle(context.Background(), refunded(map[string]any{"orderId": "O-1", "refundedTotalVnd": "lots"})); err != nil || len(f.got) != 0 {
		t.Fatalf("bad amount: %v %v", err, f.got)
	}
	f.err = dependency.Wrap(errors.New("db down"))
	if err := h.Handle(context.Background(), refunded(map[string]any{"orderId": "O-1", "refundedTotalVnd": float64(1), "refundPendingVnd": float64(0), "capturedVnd": float64(1)})); !dependency.IsUnavailable(err) {
		t.Fatalf("outage = %v, want it returned so the event is retried", err)
	}
	f.err = errors.New("something permanent")
	if err := h.Handle(context.Background(), refunded(map[string]any{"orderId": "O-1", "refundedTotalVnd": float64(1), "refundPendingVnd": float64(0), "capturedVnd": float64(1)})); err != nil {
		t.Fatalf("a permanent error must not block the stream: %v", err)
	}
}
