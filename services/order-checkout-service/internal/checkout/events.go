package checkout

import (
	"github.com/hieuvu0611/commercelab-retail-be/contracts/events"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/outbox"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/orders"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/saga"
)

// Order events leave the service through the outbox. Payloads carry ids, amounts and codes only: never a name,
// phone, address or token.
const aggregateOrder = "Order"

func (s *Service) orderEvent(typ string, o orders.Order, payload map[string]any) events.DomainEvent {
	payload["orderId"], payload["userId"] = o.OrderID, o.UserID
	return outbox.NewEvent(typ, aggregateOrder, o.OrderID, s.now(), payload)
}

func (s *Service) orderCreated(o orders.Order) events.DomainEvent {
	return s.orderEvent(events.EventOrderCreated, o, map[string]any{"totalPayableVnd": o.TotalPayableVND})
}

func (s *Service) orderConfirmed(o orders.Order, capturedVND int64) events.DomainEvent {
	items := make([]map[string]any, len(o.Items))
	for i, it := range o.Items {
		items[i] = map[string]any{"skuId": it.SkuID, "quantity": it.Quantity}
	}
	return s.orderEvent(events.EventOrderConfirmed, o, map[string]any{
		"totalPayableVnd": o.TotalPayableVND, "capturedAmountVnd": capturedVND, "items": items, "warehouseId": o.WarehouseID,
	})
}

func (s *Service) orderFailed(o orders.Order, failureCode, paymentStatus string) events.DomainEvent {
	return s.orderEvent(events.EventOrderFailed, o, map[string]any{"failureCode": failureCode, "paymentStatus": paymentStatus})
}

func (s *Service) exceptionCase(o orders.Order, reason string) events.DomainEvent {
	return s.orderEvent(events.EventExceptionCaseOpened, o, map[string]any{"reason": reason})
}

// eventsFor lists the events a saga transition announces: confirmed, failed and (for the saga's exception
// effect) an exception case for support.
func (s *Service) eventsFor(o orders.Order, res saga.Result, from saga.State, payStatus, failureCode string, ev saga.Event) []events.DomainEvent {
	var out []events.DomainEvent
	switch {
	case res.State == saga.StateConfirmed && from != saga.StateConfirmed:
		out = append(out, s.orderConfirmed(o, o.TotalPayableVND))
	case res.State == saga.StateFailedClosed && from != saga.StateFailedClosed:
		out = append(out, s.orderFailed(o, failureCode, payStatus))
	}
	if hasEffect(res.Effects, saga.OpenExceptionCase) {
		reason := "PAYMENT_AMOUNT_MISMATCH"
		if _, expired := ev.(saga.HoldExpired); expired {
			reason = FailureUnknownExpired
		}
		out = append(out, s.exceptionCase(o, reason))
	}
	return out
}
