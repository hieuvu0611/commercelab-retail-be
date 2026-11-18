package checkout

import (
	"context"
	"errors"
	"log"

	"github.com/hieuvu0611/commercelab-retail-be/contracts/ids"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/orders"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/saga"
)

var (
	// ErrNotCancellable: the order is in a state that cannot be cancelled (placing, closed or delivered).
	ErrNotCancellable = errors.New("the order cannot be cancelled in its current state")
	// ErrHandedOver: the parcel was handed to the carrier, so the order cannot be cancelled any more (case 4).
	ErrHandedOver = errors.New("the order was already handed to the carrier")
	// ErrPaymentUnresolved: the payment outcome is unknown; nothing is cancelled until it is known (case 5).
	ErrPaymentUnresolved = errors.New("the payment outcome is not known yet")
	// ErrStockHandedOver: inventory refused to release committed stock because it already left the warehouse.
	ErrStockHandedOver = errors.New("the stock was already handed over")
)

// StopResult is fulfillment's answer to a stop request for an order's parcel.
type StopResult string

const (
	// StopStopped: nothing was picked, the parcel is stopped (or never existed) and can never be handed over.
	StopStopped StopResult = "STOPPED"
	// StopRequested: the parcel is being picked or packed; the warehouse must confirm the stop (SC06).
	StopRequested StopResult = "STOP_REQUESTED"
	// StopTooLate: the parcel was handed to the carrier or is beyond.
	StopTooLate StopResult = "TOO_LATE"
)

// StopRequest names the order's parcel. The lines and warehouse let fulfillment block a parcel that does not
// exist yet (its order.confirmed event is still on its way).
type StopRequest struct {
	OrderID, UserID, WarehouseID string
	Lines                        []StockLine
}

// Fulfillment arbitrates a cancellation against the warehouse. StopShipment is idempotent.
type Fulfillment interface {
	StopShipment(ctx context.Context, r StopRequest) (StopResult, error)
}

// Cancel applies the customer's cancellation (B05, P06) to the order of userID. Another customer's order looks
// missing. It is idempotent: a cancelled order is returned as it is and no effect runs again. Every effect is
// idempotent and runs before the state is stored, so a failure leaves the order as it was and the call can be
// repeated. A paid order whose parcel is being picked or packed becomes CANCEL_REQUESTED (case 3, SC06) and is
// settled later by SettleCancel. Errors: orders.ErrNotFound, ErrNotCancellable, ErrHandedOver, ErrPaymentUnresolved,
// orders.ErrStaleState and dependency errors (retryable).
func (s *Service) Cancel(ctx context.Context, userID, orderID string) (orders.Order, error) {
	defer s.locks.lock(orderID)()
	// Another instance may change the order between the read and the compare-and-set: look again, which also
	// turns "somebody else just cancelled it" into the stored result.
	for attempt := 0; ; attempt++ {
		o, err := s.Orders.Get(ctx, orderID)
		if err != nil {
			return orders.Order{}, err
		}
		if o.UserID != userID {
			return orders.Order{}, orders.ErrNotFound
		}
		moved, err := s.cancel(ctx, o)
		if errors.Is(err, orders.ErrStaleState) && attempt < maxCancelRetries {
			continue
		}
		return moved, err
	}
}

// maxCancelRetries bounds how often a cancel re-reads an order that changed under it.
const maxCancelRetries = 3

func (s *Service) cancel(ctx context.Context, o orders.Order) (orders.Order, error) {
	switch saga.State(o.Status) {
	case saga.StateCancelled:
		return s.repairRefund(ctx, o)
	case saga.StateReserved, saga.StateUnknownHold:
		return s.cancelHeld(ctx, o)
	case saga.StateConfirmed, saga.StateInFulfillment, saga.StateCancelRequested:
		return s.cancelPaid(ctx, o)
	}
	return o, ErrNotCancellable
}

// repairRefund finishes a cancellation whose refund amount was not stored (a crash between two writes).
func (s *Service) repairRefund(ctx context.Context, o orders.Order) (orders.Order, error) {
	if o.PaymentStatus != PayRefundPending || o.CapturedVND == 0 || o.RefundPendingVND == o.CapturedVND {
		return o, nil
	}
	if err := s.Orders.RecordCapture(ctx, o.OrderID, o.CapturedVND, o.CapturedVND); err != nil {
		return o, err
	}
	o.RefundPendingVND = o.CapturedVND
	return o, nil
}

// cancelHeld handles an order that waits for its payment (cases 1 and 5). The payment service is asked first:
// a capture or an unknown outcome whose callback was lost changes which case applies.
func (s *Service) cancelHeld(ctx context.Context, o orders.Order) (orders.Order, error) {
	state, found, err := s.Payments.AttemptState(ctx, o.OrderID)
	if err != nil {
		return o, err
	}
	if found && state.Status != payProcessing && state.Status != "" {
		moved, err := s.applyPaymentTo(ctx, o, PaymentReport{AttemptID: state.AttemptID, Status: state.Status, CapturedVND: state.CapturedVND})
		if err != nil {
			return o, err
		}
		if moved.Status != o.Status {
			return s.cancel(ctx, moved)
		}
		o = moved
	}
	if o.Status == string(saga.StateUnknownHold) {
		return o, ErrPaymentUnresolved
	}
	return s.cancelUnpaid(ctx, o)
}

// cancelUnpaid is case 1: the attempt is closed first (a payment that still arrives is refunded), then stock and
// voucher go back. Money that slipped in before the close is owed back as well.
func (s *Service) cancelUnpaid(ctx context.Context, o orders.Order) (orders.Order, error) {
	res, err := saga.Next(saga.StateReserved, saga.CustomerCancelled{})
	if err != nil {
		return o, err
	}
	if err := s.Payments.CloseAttemptForCancel(ctx, ids.PaymentAttemptID(o.OrderID)); err != nil {
		return o, err
	}
	s.apply(ctx, o, res.Effects)
	state, found, err := s.Payments.AttemptState(ctx, o.OrderID)
	if err != nil {
		return o, err
	}
	payStatus, owed := res.PaymentStatus, int64(0)
	if found && state.CapturedVND > 0 {
		payStatus, owed = PayRefundPending, state.CapturedVND
	}
	return s.storeCancelled(ctx, o, payStatus, owed)
}

// cancelPaid is cases 2, 3 and 4: fulfillment decides whether the parcel can still be stopped. Stopped: the
// committed stock goes back and the captured money is owed to the customer; the voucher use stays consumed.
// Being picked or packed: the order waits as CANCEL_REQUESTED for the warehouse's answer. Handed over: the
// cancellation is refused, and a waiting one is rejected (SC06 branch B).
func (s *Service) cancelPaid(ctx context.Context, o orders.Order) (orders.Order, error) {
	res, err := saga.Next(saga.State(o.Status), saga.CustomerCancelled{})
	if err != nil {
		return o, err
	}
	if s.Fulfillment == nil {
		return o, dependency.Wrap(errors.New("fulfillment is not configured"))
	}
	lines := make([]StockLine, len(o.Items))
	for i, it := range o.Items {
		lines[i] = StockLine{SkuID: it.SkuID, Quantity: it.Quantity}
	}
	result, err := s.Fulfillment.StopShipment(ctx, StopRequest{OrderID: o.OrderID, UserID: o.UserID, WarehouseID: o.WarehouseID, Lines: lines})
	if err != nil {
		return o, err
	}
	switch result {
	case StopStopped:
	case StopRequested:
		return s.awaitStop(ctx, o)
	case StopTooLate:
		if o.Status == string(saga.StateCancelRequested) {
			rejected, err := s.rejectCancel(ctx, o)
			if err != nil {
				return o, err
			}
			return rejected, ErrHandedOver
		}
		return o, ErrHandedOver
	default:
		return o, dependency.Wrap(errors.New("fulfillment answered an unknown stop result"))
	}
	if hasEffect(res.Effects, saga.ReleaseCommittedStock) {
		if err := s.Stock.ReleaseCommitted(ctx, o.OrderID); errors.Is(err, ErrStockHandedOver) {
			return o, ErrHandedOver
		} else if err != nil {
			return o, err
		}
	}
	if err := s.Payments.CloseAttemptForCancel(ctx, ids.PaymentAttemptID(o.OrderID)); err != nil {
		return o, err
	}
	if o.PaymentMethod == PaymentMethodCOD && o.CapturedVND == 0 {
		return s.storeCancelled(ctx, o, PayCODCancelled, 0)
	}
	owed := o.CapturedVND
	if owed == 0 {
		owed = o.TotalPayableVND // a confirmed order captured exactly its total (P04)
	}
	return s.storeCancelled(ctx, o, res.PaymentStatus, owed)
}

// storeCancelled stores the CANCELLED state with its event (compare-and-set on the current state) and then the
// amount owed back.
func (s *Service) storeCancelled(ctx context.Context, o orders.Order, payStatus string, owedVND int64) (orders.Order, error) {
	moved, err := s.Orders.Transition(ctx, o.OrderID, o.Status, string(saga.StateCancelled), payStatus, o.FailureCode,
		s.orderCancelled(o, payStatus, owedVND))
	if err != nil {
		return o, err
	}
	if owedVND > 0 {
		if err := s.Orders.RecordCapture(ctx, o.OrderID, owedVND, owedVND); err != nil {
			return moved, err
		}
		moved.CapturedVND, moved.RefundPendingVND = owedVND, owedVND
	}
	return moved, nil
}

// awaitStop stores CANCEL_REQUESTED (an order that already waits stays as it is) and announces the request.
func (s *Service) awaitStop(ctx context.Context, o orders.Order) (orders.Order, error) {
	if o.Status == string(saga.StateCancelRequested) {
		return o, nil
	}
	res, err := saga.Next(saga.State(o.Status), saga.CancelRequested{})
	if err != nil {
		return o, err
	}
	return s.Orders.Transition(ctx, o.OrderID, o.Status, string(res.State), res.PaymentStatus, o.FailureCode, s.orderCancelRequested(o))
}

// rejectCancel puts a waiting order back into fulfillment because the parcel was handed over first (SC06 B).
func (s *Service) rejectCancel(ctx context.Context, o orders.Order) (orders.Order, error) {
	res, err := saga.Next(saga.StateCancelRequested, saga.CancelRejected{})
	if err != nil {
		return o, err
	}
	return s.Orders.Transition(ctx, o.OrderID, o.Status, string(res.State), res.PaymentStatus, o.FailureCode, s.orderCancelRejected(o))
}

// SettleCancel applies the warehouse's answer to an order that waits in CANCEL_REQUESTED: the stopped parcel
// cancels it (stock, refund), a handed over parcel rejects the cancellation. It asks fulfillment, which keeps
// the answer, so it is the same call for a stopped or handed over event and for the sweeper, and repeating it is
// harmless. Any other order is left alone. Only retryable (dependency) errors are returned.
func (s *Service) SettleCancel(ctx context.Context, orderID string) error {
	_, err := s.settleCancel(ctx, orderID)
	return err
}

// settleCancel reports whether the order left CANCEL_REQUESTED.
func (s *Service) settleCancel(ctx context.Context, orderID string) (bool, error) {
	defer s.locks.lock(orderID)()
	o, err := s.Orders.Get(ctx, orderID)
	if errors.Is(err, orders.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if o.Status != string(saga.StateCancelRequested) {
		return false, nil
	}
	moved, err := s.cancelPaid(ctx, o)
	if errors.Is(err, ErrHandedOver) || errors.Is(err, orders.ErrStaleState) {
		err = nil // the rejection is stored, or another instance already settled it
	}
	return moved.Status != o.Status, err
}

// RefundReport is the payment service's view after a payout: the totals refunded and still owed, and what was
// captured. The values are absolute, so a repeated or reordered report changes nothing.
type RefundReport struct{ RefundedVND, PendingVND, CapturedVND int64 }

// refundSettled reports whether money was already paid back, which a replayed payment report must not undo.
func refundSettled(paymentStatus string) bool {
	return paymentStatus == PayRefunded || paymentStatus == PayPartiallyRefunded
}

// ApplyRefund stores a payout on the order: REFUNDED when nothing is owed any more, PARTIALLY_REFUNDED while some
// is. A report that would refund more than was captured, or is negative, is dropped (it can never become valid,
// and retrying it would block the event stream); an unknown order is skipped. Only retryable errors are returned.
func (s *Service) ApplyRefund(ctx context.Context, orderID string, r RefundReport) error {
	defer s.locks.lock(orderID)()
	o, err := s.Orders.Get(ctx, orderID)
	if errors.Is(err, orders.ErrNotFound) {
		log.Printf("[order] refund report names unknown order %q; skipped", orderID)
		return nil
	}
	if err != nil {
		return err
	}
	ceiling := max(o.CapturedVND, r.CapturedVND)
	if r.RefundedVND < 0 || r.PendingVND < 0 || r.RefundedVND+r.PendingVND > ceiling {
		log.Printf("[order] refund report for order %s is above the captured amount; ignored", orderID)
		return nil
	}
	status := o.PaymentStatus
	switch {
	case r.RefundedVND > 0 && r.PendingVND == 0:
		status = PayRefunded
	case r.RefundedVND > 0:
		status = PayPartiallyRefunded
	}
	if o.CapturedVND < ceiling {
		if err := s.Orders.RecordCapture(ctx, orderID, ceiling, r.PendingVND); err != nil {
			return err
		}
	}
	return s.Orders.RecordRefund(ctx, orderID, r.RefundedVND, r.PendingVND, status)
}
