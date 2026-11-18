package checkout

import (
	"context"
	"errors"
	"hash/fnv"
	"log"
	"sync"

	"github.com/hieuvu0611/commercelab-retail-be/contracts/ids"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/orders"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/saga"
)

var (
	// ErrAttemptMismatch: the report names a payment attempt that is not the order's attempt.
	ErrAttemptMismatch = errors.New("payment attempt does not belong to the order")
	// ErrUnknownPaymentStatus: the payment service reported a status the order does not understand.
	ErrUnknownPaymentStatus = errors.New("unknown payment status")
)

// Payment attempt statuses as reported by the payment service.
const (
	payProcessing   = "PROCESSING"
	payUnknown      = "UNKNOWN"
	payCaptured     = "CAPTURED"
	payFailed       = "FAILED"
	payExpired30m   = "EXPIRED_30M"
	payCapturedLate = "CAPTURED_LATE"
	payCODPending   = "COD_PENDING"
	payCODCollected = "COLLECTED"
	payCODCancelled = "COD_CANCELLED"
)

// PaymentReport is what the payment service says about an order's attempt. The expected amount is never part of
// it: it is always the order's own total (P04).
type PaymentReport struct {
	AttemptID   string
	Status      string
	CapturedVND int64
}

// lockStripes serialise the saga steps of one order inside this process: a payment callback and the sweeper must
// not run opposite effects (commit versus release) for the same order at the same time.
const lockStripes = 64

type orderLocks [lockStripes]sync.Mutex

func (l *orderLocks) lock(orderID string) func() {
	h := fnv.New32a()
	_, _ = h.Write([]byte(orderID))
	m := &l[h.Sum32()%lockStripes]
	m.Lock()
	return m.Unlock
}

// ApplyPayment applies a payment report to the order saga: confirm on an equal capture, close on a definite
// failure, hold on an unknown outcome, and refund money that arrives after the order closed. Every effect is
// idempotent and reports repeat harmlessly. Errors leave the order as it was so the report can be retried;
// dependency errors are retryable, saga.ErrInvalidTransition is not.
func (s *Service) ApplyPayment(ctx context.Context, orderID string, r PaymentReport) (orders.Order, error) {
	if r.AttemptID != ids.PaymentAttemptID(orderID) {
		return orders.Order{}, ErrAttemptMismatch
	}
	defer s.locks.lock(orderID)()
	o, err := s.Orders.Get(ctx, orderID)
	if err != nil {
		return orders.Order{}, err
	}
	return s.applyPaymentTo(ctx, o, r)
}

// applyPaymentTo is ApplyPayment for a caller that already holds the order's lock and has read the order.
func (s *Service) applyPaymentTo(ctx context.Context, o orders.Order, r PaymentReport) (orders.Order, error) {
	events, err := paymentEvents(o, r)
	if err != nil {
		return o, err
	}
	for _, ev := range events {
		if o, err = s.step(ctx, o, ev); err != nil {
			return o, err
		}
	}
	return s.recordCapture(ctx, o, r)
}

// paymentEvents translates a payment status into saga events for the order's current state.
func paymentEvents(o orders.Order, r PaymentReport) ([]saga.Event, error) {
	held := o.Status == string(saga.StateReserved) || o.Status == string(saga.StateUnknownHold)
	cancelled := o.Status == string(saga.StateCancelled) || o.Status == string(saga.StateCancelRequested)
	// A cancelled order only owes money that arrived after an unpaid cancellation; every other report (replays
	// included) says nothing new, and the refund is never created twice.
	lateMoney := o.Status == string(saga.StateCancelled) && o.CapturedVND == 0
	switch r.Status {
	case payProcessing, payCODPending, payCODCollected, payCODCancelled:
		return nil, nil
	case payCaptured, payCapturedLate:
		if cancelled && !lateMoney {
			return nil, nil
		}
	case payFailed, payUnknown, payExpired30m:
		if cancelled {
			return nil, nil
		}
	}
	switch r.Status {
	case payProcessing, payCODPending, payCODCollected, payCODCancelled:
		return nil, nil
	case payCaptured:
		return []saga.Event{saga.PaymentCaptured{CapturedVND: r.CapturedVND, ExpectedVND: o.TotalPayableVND}}, nil
	case payFailed:
		return []saga.Event{saga.PaymentFailed{}}, nil
	case payUnknown:
		if saga.State(o.Status).IsPaid() || o.Status == string(saga.StateFailedClosed) {
			return nil, nil // a late "unknown" says nothing new about an order that is already decided
		}
		return []saga.Event{saga.PaymentUnknown{}}, nil
	case payExpired30m:
		if held {
			return []saga.Event{saga.HoldExpired{}}, nil
		}
		return nil, nil
	case payCapturedLate:
		// The payment service decided the capture was late: the order is closed first and the money is refunded.
		if held {
			return []saga.Event{saga.HoldExpired{}, saga.PaymentCaptured{CapturedVND: r.CapturedVND, ExpectedVND: o.TotalPayableVND}}, nil
		}
		return []saga.Event{saga.PaymentCaptured{CapturedVND: r.CapturedVND, ExpectedVND: o.TotalPayableVND}}, nil
	}
	return nil, ErrUnknownPaymentStatus
}

// step applies one saga event: effects first (they are idempotent, so a crash is repaired by a retry), then the
// compare-and-set of the new state. A step that changes nothing stores nothing.
func (s *Service) step(ctx context.Context, o orders.Order, ev saga.Event) (orders.Order, error) {
	from := saga.State(o.Status)
	res, err := saga.Next(from, ev)
	if err != nil {
		return o, err
	}
	payStatus, failure := res.PaymentStatus, failureAfter(o, res, ev)
	if hasEffect(res.Effects, saga.RecordRefundObligation) ||
		(res.State == saga.StateFailedClosed && from != saga.StateFailedClosed && o.CapturedVND > 0) {
		payStatus = PayRefundPending // money is held for a closed order: it goes back to the customer
	}
	if refundSettled(o.PaymentStatus) {
		payStatus = o.PaymentStatus // a replayed report never undoes a payout
	}
	if lost, err := s.runEffects(ctx, o, res.Effects); lost {
		return s.closeLost(ctx, o, from)
	} else if err != nil {
		return o, err
	}
	if res.State == saga.StateFailedClosed && from != saga.StateFailedClosed {
		if err := s.Payments.CloseAttempt(ctx, ids.PaymentAttemptID(o.OrderID)); err != nil {
			return o, err
		}
	}
	if res.State == from && payStatus == o.PaymentStatus {
		return o, nil
	}
	moved, err := s.Orders.Transition(ctx, o.OrderID, string(from), string(res.State), payStatus, failure,
		s.eventsFor(o, res, from, payStatus, failure, ev)...)
	if err != nil {
		return o, err
	}
	if res.State == saga.StateConfirmed && from != saga.StateConfirmed {
		s.clearCart(ctx, moved)
	}
	if payStatus == PayRefundPending && moved.CapturedVND > 0 && moved.RefundPendingVND != moved.CapturedVND {
		if err := s.Orders.RecordCapture(ctx, moved.OrderID, moved.CapturedVND, moved.CapturedVND); err != nil {
			return moved, err
		}
		moved.RefundPendingVND = moved.CapturedVND
	}
	return moved, nil
}

// failureAfter is the failure code stored when the order closes; an order that stays as it is keeps its code.
func failureAfter(o orders.Order, res saga.Result, ev saga.Event) string {
	if res.State != saga.StateFailedClosed || o.Status == string(saga.StateFailedClosed) {
		return o.FailureCode
	}
	switch ev.(type) {
	case saga.PaymentFailed:
		return FailurePaymentFailed
	case saga.HoldExpired:
		if o.Status == string(saga.StateUnknownHold) {
			return FailureUnknownExpired
		}
		return FailureReservationExpired
	}
	return o.FailureCode
}

func hasEffect(effects []saga.Effect, want saga.Effect) bool {
	for _, e := range effects {
		if e == want {
			return true
		}
	}
	return false
}

// runEffects performs the effects of a payment transition. lost=true means the stock reservation is gone, so the
// order cannot be confirmed. A non-nil error is retryable.
func (s *Service) runEffects(ctx context.Context, o orders.Order, effects []saga.Effect) (lost bool, err error) {
	for _, e := range effects {
		switch e {
		case saga.CommitStock:
			if err := s.Stock.Commit(ctx, o.OrderID); errors.Is(err, ErrReservationLost) {
				return true, nil
			} else if err != nil {
				return false, err
			}
		case saga.ConsumeVoucher:
			if o.VoucherCode != "" {
				if err := s.Vouchers.Consume(ctx, o.VoucherCode, o.OrderID); err != nil {
					return false, err
				}
			}
			if s.Promotions != nil && len(o.AppliedPromotions) > 0 {
				if err := s.Promotions.ConsumeOrder(ctx, o.OrderID); err != nil {
					return false, err
				}
			}
		case saga.HoldStockException:
			if err := s.Stock.ExceptionHold(ctx, o.OrderID); errors.Is(err, ErrReservationLost) {
				log.Printf("[order] exception hold for order %s: the reservation is already gone", o.OrderID)
			} else if err != nil {
				return false, err
			}
		case saga.ReleaseStock:
			s.releaseStock(ctx, o.OrderID)
		case saga.ReleaseVoucher:
			s.releaseVoucher(ctx, o)
		case saga.RecordRefundObligation:
			if err := s.Payments.CloseAttempt(ctx, ids.PaymentAttemptID(o.OrderID)); err != nil {
				return false, err
			}
		case saga.OpenExceptionCase:
			// The exception case is raised through the outbox (P5-T08).
		}
	}
	return false, nil
}

// closeLost closes an order whose money was captured but whose stock reservation is gone: nothing can be shipped,
// so the voucher goes back and the money is owed back to the customer.
func (s *Service) closeLost(ctx context.Context, o orders.Order, from saga.State) (orders.Order, error) {
	s.releaseVoucher(ctx, o)
	if err := s.Payments.CloseAttempt(ctx, ids.PaymentAttemptID(o.OrderID)); err != nil {
		return o, err
	}
	return s.Orders.Transition(ctx, o.OrderID, string(from), string(saga.StateFailedClosed), PayRefundPending, FailureStockLost,
		s.orderFailed(o, FailureStockLost, PayRefundPending))
}

// recordCapture stores the captured and refundable amounts the report carries.
func (s *Service) recordCapture(ctx context.Context, o orders.Order, r PaymentReport) (orders.Order, error) {
	if r.Status == payCODPending && r.CapturedVND > 0 {
		if err := s.Orders.RecordCapture(ctx, o.OrderID, r.CapturedVND, 0); err != nil {
			return o, err
		}
		o.CapturedVND = r.CapturedVND
		return o, nil
	}
	if r.Status == payCODCollected {
		captured := r.CapturedVND
		if captured <= 0 {
			captured = o.TotalPayableVND
		}
		if err := s.Orders.RecordRefund(ctx, o.OrderID, 0, 0, payCODCollected); err != nil {
			return o, err
		}
		if err := s.Orders.RecordCapture(ctx, o.OrderID, captured, 0); err != nil {
			return o, err
		}
		o.PaymentStatus = payCODCollected
		o.CapturedVND = captured
		return o, nil
	}
	if r.Status == payCODCancelled {
		if err := s.Orders.RecordRefund(ctx, o.OrderID, 0, 0, payCODCancelled); err != nil {
			return o, err
		}
		o.PaymentStatus = payCODCancelled
		return o, nil
	}
	if r.Status != payCaptured && r.Status != payCapturedLate {
		return o, nil
	}
	var refund int64
	if o.PaymentStatus == PayRefundPending {
		refund = r.CapturedVND
	}
	if o.CapturedVND == r.CapturedVND && o.RefundPendingVND == refund {
		return o, nil
	}
	if err := s.Orders.RecordCapture(ctx, o.OrderID, r.CapturedVND, refund); err != nil {
		return o, err
	}
	o.CapturedVND, o.RefundPendingVND = r.CapturedVND, refund
	return o, nil
}

func (s *Service) clearCart(ctx context.Context, o orders.Order) {
	if s.Carts == nil {
		return
	}
	skus := make([]string, len(o.Items))
	for i, it := range o.Items {
		skus[i] = it.SkuID
	}
	if err := s.Carts.RemoveOrdered(ctx, o.UserID, skus); err != nil {
		log.Printf("[order] cart cleanup failed for confirmed order %s: %s", o.OrderID, redact(err))
	}
}
