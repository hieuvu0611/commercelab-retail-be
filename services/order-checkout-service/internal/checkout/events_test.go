package checkout

import (
	"fmt"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/contracts/events"
)

func emitted(f *fixture) []events.DomainEvent { return f.orders.Outbox() }

func types(evs []events.DomainEvent) string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = e.EventType
	}
	return fmt.Sprint(out)
}

func TestPlacingAnOrderEmitsOrderCreated(t *testing.T) {
	f := newFixture()
	o := placed(t, f)
	evs := emitted(f)
	if types(evs) != "[order.created.v1]" || evs[0].AggregateType != "Order" || evs[0].AggregateID != o.OrderID {
		t.Fatalf("events = %+v", evs)
	}
	if evs[0].Payload["orderId"] != o.OrderID || evs[0].Payload["totalPayableVnd"] != int64(660000) || evs[0].EventID == "" {
		t.Fatalf("payload = %+v", evs[0].Payload)
	}
}

func TestAnOrderThatCannotBePlacedEmitsOrderFailedWithTheCode_SC03(t *testing.T) {
	f := newFixture()
	f.stock.reserveFn = func() error { return ErrOutOfStock }
	o, _, _ := place(t, f, lan, "key-1", "Q-1")
	evs := emitted(f)
	if types(evs) != "[order.created.v1 order.failed.v1]" || evs[1].Payload["failureCode"] != "OUT_OF_STOCK" || evs[1].AggregateID != o.OrderID {
		t.Fatalf("events = %+v", evs)
	}
}

func TestConfirmationEmitsOrderConfirmedOnce(t *testing.T) {
	f := newFixture()
	o := placed(t, f)
	for i := 0; i < 3; i++ {
		f.apply(t, o, "CAPTURED", 660000)
	}
	evs := emitted(f)
	if types(evs) != "[order.created.v1 order.confirmed.v1]" {
		t.Fatalf("events = %s", types(evs))
	}
	items, ok := evs[1].Payload["items"].([]map[string]any)
	if !ok || len(items) != 2 || items[0]["skuId"] != "SKU-BALO-BLACK-20L" || items[0]["quantity"] != int64(2) || evs[1].Payload["capturedAmountVnd"] != int64(660000) {
		t.Fatalf("payload = %+v", evs[1].Payload)
	}
}

func TestPaymentFailureEmitsOrderFailed(t *testing.T) {
	f := newFixture()
	o := placed(t, f)
	f.apply(t, o, "FAILED", 0)
	f.apply(t, o, "FAILED", 0)
	evs := emitted(f)
	if types(evs) != "[order.created.v1 order.failed.v1]" || evs[1].Payload["failureCode"] != FailurePaymentFailed {
		t.Fatalf("events = %+v", evs)
	}
}

func TestAnAmountMismatchOpensAnExceptionCase_P04(t *testing.T) {
	f := newFixture()
	o := placed(t, f)
	f.apply(t, o, "CAPTURED", 650000)
	evs := emitted(f)
	if types(evs) != "[order.created.v1 support.exception_case_opened.v1]" || evs[1].Payload["reason"] != "PAYMENT_AMOUNT_MISMATCH" || evs[1].Payload["orderId"] != o.OrderID {
		t.Fatalf("events = %+v", evs)
	}
}

func TestAnExpiredUnknownPaymentEmitsFailedAndAnExceptionCase_SC05(t *testing.T) {
	f := newClockFixture(at(10, 0))
	o := placed(t, f.fixture)
	f.apply(t, o, "UNKNOWN", 0)
	f.payments.states[o.OrderID] = PaymentState{AttemptID: "PAY-" + o.OrderID, Status: "UNKNOWN"}
	f.sweep(t, 10, 31)
	evs := emitted(f.fixture)
	if types(evs) != "[order.created.v1 order.failed.v1 support.exception_case_opened.v1]" ||
		evs[1].Payload["failureCode"] != FailureUnknownExpired || evs[2].Payload["reason"] != FailureUnknownExpired {
		t.Fatalf("events = %+v", evs)
	}
}

func TestEventsCarryNoPersonalData(t *testing.T) {
	f := newFixture()
	o := placed(t, f)
	f.apply(t, o, "CAPTURED", 660000)
	allowed := map[string]bool{"orderId": true, "userId": true, "totalPayableVnd": true, "capturedAmountVnd": true, "items": true,
		"failureCode": true, "paymentStatus": true, "reason": true}
	for _, e := range emitted(f) {
		for k := range e.Payload {
			if !allowed[k] {
				t.Errorf("%s carries unexpected field %q", e.EventType, k)
			}
		}
	}
}
