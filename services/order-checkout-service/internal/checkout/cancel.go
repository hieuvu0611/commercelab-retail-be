package checkout

import (
	"context"
	"errors"

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
	// ErrBeingPacked: the warehouse is picking or packing the parcel (case 3, accepted from P7-T02 on).
	ErrBeingPacked = errors.New("the warehouse is already packing the order")
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
	// StopInProgress: the parcel is being picked or packed and stop requests are not accepted yet.
	StopInProgress StopResult = "IN_PROGRESS"
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
// repeated. Errors: orders.ErrNotFound, ErrNotCancellable, ErrHandedOver, ErrPaymentUnresolved, ErrBeingPacked,
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
	case saga.StateConfirmed, saga.StateInFulfillment:
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
	if err := s.Payments.CloseAttempt(ctx, ids.PaymentAttemptID(o.OrderID)); err != nil {
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

// cancelPaid is cases 2 and 4 (and case 3 from P7-T02): fulfillment decides whether the parcel can still be
// stopped. Only then the committed stock goes back and the captured money is owed to the customer; the voucher
// use stays consumed.
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
	case StopTooLate:
		return o, ErrHandedOver
	default:
		return o, ErrBeingPacked
	}
	if hasEffect(res.Effects, saga.ReleaseCommittedStock) {
		if err := s.Stock.ReleaseCommitted(ctx, o.OrderID); errors.Is(err, ErrStockHandedOver) {
			return o, ErrHandedOver
		} else if err != nil {
			return o, err
		}
	}
	if err := s.Payments.CloseAttempt(ctx, ids.PaymentAttemptID(o.OrderID)); err != nil {
		return o, err
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
