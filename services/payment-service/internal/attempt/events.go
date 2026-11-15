package attempt

import (
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/contracts/events"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/outbox"
)

const aggregatePayment = "Payment"

// EventsFor lists the events announced by a change of an attempt from cur to next: money captured, an unknown
// outcome that opens the 30-minute window, and money owed back. Payloads carry ids and amounts only. The
// aggregate id is the order id, so every event of one order is ordered on the same partition.
func EventsFor(cur, next Attempt, ch Change, at time.Time) []events.DomainEvent {
	var out []events.DomainEvent
	event := func(typ string, payload map[string]any) {
		payload["orderId"], payload["attemptId"] = next.OrderID, next.AttemptID
		out = append(out, outbox.NewEvent(typ, aggregatePayment, next.OrderID, at, payload))
	}
	if ch.CapturedVND > 0 {
		event(events.EventPaymentCaptured, map[string]any{
			"amountVnd": ch.CapturedVND, "expectedAmountVnd": next.AmountVND, "late": next.Status == StatusCapturedLate,
		})
	}
	if next.Status == StatusUnknown && cur.Status != StatusUnknown && next.UnknownDeadline != nil {
		event(events.EventPaymentUnknown30m, map[string]any{"unknownDeadline": next.UnknownDeadline.UTC().Format(time.RFC3339)})
	}
	if ch.LateRefundVND > 0 {
		event(events.EventPaymentLateRefundNeed, map[string]any{"amountVnd": ch.LateRefundVND})
	}
	return out
}
