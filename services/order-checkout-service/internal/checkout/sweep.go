package checkout

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/contracts/ids"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/orders"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/saga"
)

// Sweeper timings. The clock is injected, so tests move it instead of waiting.
const (
	// UnknownHoldTTL is how long an order with an unknown payment keeps its stock (P05).
	UnknownHoldTTL = 30 * time.Minute
	// PendingGrace is how long a PENDING order may still be in the middle of being placed.
	PendingGrace = 2 * time.Minute
	// ReconcileAfter is how old a held order must be before the sweeper asks the payment service about it.
	ReconcileAfter = 30 * time.Second
	// OrphanWindow is how long after a dependency failure the sweeper keeps closing a possibly orphaned attempt.
	OrphanWindow = 10 * time.Minute
	// VoucherGrace is how long a reserved voucher use is left alone before it is checked against its order.
	VoucherGrace = time.Minute
	// SweepBatch bounds the work of one tick per category.
	SweepBatch = 100
)

// SweepStats counts what one sweep did.
type SweepStats struct {
	Interrupted, Reconciled, Expired, AttemptsClosed, VouchersReleased int
}

// Sweep repairs and times out orders. It is safe to run repeatedly and concurrently with payment callbacks:
// every action is idempotent and the saga steps of one order are serialised. It never fails as a whole; a
// problem with one order is logged and retried on the next tick.
func (s *Service) Sweep(ctx context.Context) SweepStats {
	var st SweepStats
	now := s.now()
	for _, o := range s.list(ctx, orders.Filter{Statuses: []string{orders.StatusPending}, UpdatedBefore: now.Add(-PendingGrace)}) {
		if s.interrupt(ctx, o.OrderID) {
			st.Interrupted++
		}
	}
	held := []string{string(saga.StateReserved), string(saga.StateUnknownHold)}
	for _, o := range s.list(ctx, orders.Filter{Statuses: held, UpdatedBefore: now.Add(-ReconcileAfter)}) {
		reconciled, expired := s.settleHeld(ctx, o.OrderID)
		if reconciled {
			st.Reconciled++
		}
		if expired {
			st.Expired++
		}
	}
	orphans := orders.Filter{Statuses: []string{string(saga.StateFailedClosed)}, FailureCode: FailureDependencyUnavailable,
		UpdatedBefore: now.Add(-ReconcileAfter), UpdatedAfter: now.Add(-OrphanWindow)}
	for _, o := range s.list(ctx, orphans) {
		if err := s.Payments.CloseAttempt(ctx, ids.PaymentAttemptID(o.OrderID)); err != nil {
			log.Printf("[order] sweep: closing the attempt of order %s failed: %s", o.OrderID, redact(err))
			continue
		}
		st.AttemptsClosed++
	}
	st.VouchersReleased = s.reconcileVouchers(ctx, now)
	return st
}

func (s *Service) list(ctx context.Context, f orders.Filter) []orders.Order {
	f.Limit = SweepBatch
	list, _, err := s.Orders.Query(ctx, f)
	if err != nil {
		log.Printf("[order] sweep: listing orders failed: %s", redact(err))
		return nil
	}
	return list
}

// interrupt closes a PENDING order whose placement never finished (a crash). Whatever it may have taken is given
// back: the voucher through the saga effect and the stock explicitly (a reservation may exist without the order
// knowing). Both releases are idempotent.
func (s *Service) interrupt(ctx context.Context, orderID string) bool {
	defer s.locks.lock(orderID)()
	o, err := s.Orders.Get(ctx, orderID)
	if err != nil || o.Status != orders.StatusPending {
		return false
	}
	s.releaseStock(ctx, o.OrderID)
	closed := s.close(ctx, o, saga.StatePending, saga.ReserveFailed{}, FailurePlacementInterrupted)
	return closed.Status == string(saga.StateFailedClosed)
}

// settleHeld first asks the payment service where the order's payment stands (a capture or a failure whose
// callback was lost is applied now), then ends the hold if its time is up. When the payment service cannot be
// asked nothing is released: a capture may have happened.
func (s *Service) settleHeld(ctx context.Context, orderID string) (reconciled, expired bool) {
	o, err := s.Orders.Get(ctx, orderID)
	if err != nil {
		return false, false
	}
	state, found, err := s.Payments.AttemptState(ctx, orderID)
	if err != nil {
		log.Printf("[order] sweep: payment state of order %s unavailable: %s", orderID, redact(err))
		return false, false
	}
	if found && state.Status != payProcessing && state.Status != "" {
		before := o
		o, err = s.ApplyPayment(ctx, orderID, PaymentReport{AttemptID: state.AttemptID, Status: state.Status, CapturedVND: state.CapturedVND})
		if err != nil {
			log.Printf("[order] sweep: applying the payment state of order %s failed: %v", orderID, err)
			return false, false
		}
		reconciled = o.Status != before.Status || o.PaymentStatus != before.PaymentStatus
	}
	return reconciled, s.expire(ctx, orderID)
}

// expire ends the hold of an order whose time is up: 10 minutes for RESERVED, 30 for UNKNOWN_HOLD.
func (s *Service) expire(ctx context.Context, orderID string) bool {
	defer s.locks.lock(orderID)()
	o, err := s.Orders.Get(ctx, orderID)
	if err != nil || !holdIsOver(o, s.now()) {
		return false
	}
	if _, err := s.step(ctx, o, saga.HoldExpired{}); err != nil {
		if !errors.Is(err, orders.ErrStaleState) {
			log.Printf("[order] sweep: ending the hold of order %s failed: %s", orderID, redact(err))
		}
		return false
	}
	return true
}

func holdIsOver(o orders.Order, now time.Time) bool {
	switch saga.State(o.Status) {
	case saga.StateReserved:
		return o.ExpiresAt != nil && !now.Before(*o.ExpiresAt)
	case saga.StateUnknownHold:
		return !now.Before(o.UpdatedAt.Add(UnknownHoldTTL))
	}
	return false
}

// reconcileVouchers gives back the voucher uses whose order is closed or gone (a release that failed or a crash
// between the voucher and the order) and consumes the use of an order that was confirmed.
func (s *Service) reconcileVouchers(ctx context.Context, now time.Time) int {
	uses, err := s.Vouchers.ReservedBefore(ctx, now.Add(-VoucherGrace), SweepBatch)
	if err != nil {
		log.Printf("[order] sweep: listing reserved vouchers failed: %s", redact(err))
		return 0
	}
	released := 0
	for _, u := range uses {
		o, err := s.Orders.Get(ctx, u.OrderID)
		switch {
		case errors.Is(err, orders.ErrNotFound) || (err == nil && o.Status == string(saga.StateFailedClosed)):
			if err := s.Vouchers.Release(ctx, u.Code, u.OrderID); err != nil {
				log.Printf("[order] sweep: voucher release for order %s failed: %s", u.OrderID, redact(err))
				continue
			}
			released++
		case err == nil && o.Status == string(saga.StateCancelled) && o.CapturedVND == 0:
			// An unpaid cancellation gives the use back; a paid one keeps it consumed (P12).
			if err := s.Vouchers.Release(ctx, u.Code, u.OrderID); err != nil {
				log.Printf("[order] sweep: voucher release for order %s failed: %s", u.OrderID, redact(err))
				continue
			}
			released++
		case err == nil && (saga.State(o.Status).IsPaid() || o.Status == string(saga.StateCancelled) || o.Status == string(saga.StateCancelRequested)):
			if err := s.Vouchers.Consume(ctx, u.Code, u.OrderID); err != nil {
				log.Printf("[order] sweep: voucher consume for order %s failed: %s", u.OrderID, redact(err))
			}
		}
	}
	return released
}

// RunSweeper sweeps every interval until ctx is cancelled.
func (s *Service) RunSweeper(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if st := s.Sweep(ctx); st != (SweepStats{}) {
				log.Printf("[order] sweep: %+v", st)
			}
		}
	}
}
