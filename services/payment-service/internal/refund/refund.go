// Package refund pays out refund obligations through the payment provider (the simulator in development) and
// records the result. A refund is identified by its attempt id, which is also the provider's idempotency key, so
// repeating a run, running two workers, or retrying after a timeout never pays a customer twice (I03, SC08).
package refund

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/services/payment-service/internal/attempt"
)

// Provider pays money back to the customer. Refund is idempotent per refundID: asking again returns the
// reference of the first payout and pays nothing more.
type Provider interface {
	Refund(ctx context.Context, refundID string, amountVND int64) (providerRef string, err error)
}

// Simulator is the development payment provider: it accepts every valid refund and remembers it.
type Simulator struct {
	mu      sync.Mutex
	payouts map[string]int64
}

func NewSimulator() *Simulator { return &Simulator{payouts: map[string]int64{}} }

// Refund implements Provider. The same id with another amount is refused instead of silently paying either.
func (s *Simulator) Refund(_ context.Context, refundID string, amountVND int64) (string, error) {
	if refundID == "" || amountVND <= 0 {
		return "", errors.New("the refund needs an id and a positive amount")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if paid, ok := s.payouts[refundID]; ok {
		if paid != amountVND {
			return "", fmt.Errorf("refund %s was already paid with another amount", refundID)
		}
		return "SIM-RF-" + refundID, nil
	}
	s.payouts[refundID] = amountVND
	return "SIM-RF-" + refundID, nil
}

// Payouts is the number of distinct refunds paid; PaidVND their total (tests and demos).
func (s *Simulator) Payouts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.payouts)
}

func (s *Simulator) PaidVND() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var total int64
	for _, v := range s.payouts {
		total += v
	}
	return total
}

// batchSize bounds the obligations one run handles.
const batchSize = 100

// Worker pays pending obligations.
type Worker struct {
	Store    attempt.Store
	Provider Provider
}

// RunOnce pays every pending obligation it can and returns how many were completed. An obligation above what
// the order still has pending is never sent to the provider; a provider failure leaves the obligation pending
// for the next run. Problems with one obligation are logged and do not stop the others.
func (w *Worker) RunOnce(ctx context.Context) (int, error) {
	list, err := w.Store.PendingRefunds(ctx, batchSize)
	if err != nil {
		return 0, err
	}
	done := 0
	for _, o := range list {
		sum, err := w.Store.Summary(ctx, o.OrderID)
		if err != nil {
			log.Printf("[payment] refund of attempt %s: the order's totals are unavailable: %v", o.AttemptID, err)
			continue
		}
		// I03: what is paid out never exceeds what was captured. The pending amount is already inside that ceiling.
		if o.AmountVND <= 0 || o.AmountVND > sum.RefundPending || sum.RefundedVND+o.AmountVND > sum.CapturedVND {
			log.Printf("[payment] refund of attempt %s is above the order's refundable amount; not paid", o.AttemptID)
			continue
		}
		ref, err := w.Provider.Refund(ctx, o.AttemptID, o.AmountVND)
		if err != nil {
			log.Printf("[payment] provider refused or timed out for attempt %s; it stays pending: %v", o.AttemptID, err)
			continue
		}
		if _, _, completed, err := w.Store.CompleteRefund(ctx, o.AttemptID, ref); err != nil {
			log.Printf("[payment] recording the refund of attempt %s failed (the provider pays once per attempt, so a retry is safe): %v", o.AttemptID, err)
		} else if completed {
			done++
		}
	}
	return done, nil
}

// Run calls RunOnce every interval until ctx ends.
func (w *Worker) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := w.RunOnce(ctx); err != nil && ctx.Err() == nil {
				log.Printf("[payment] refund run failed: %v", err)
			}
		}
	}
}
