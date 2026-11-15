package orders

import (
	"context"
	"sync"
	"time"
)

type memKey struct{ user, key string }

type memEntry struct {
	hash    string
	orderID string
}

// MemoryStore is the dev/test store used when Postgres is absent (ALLOW_MEMORY_FALLBACK=true).
type MemoryStore struct {
	mu     sync.Mutex
	keys   map[memKey]memEntry
	orders map[string]Order
	now    func() time.Time
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{keys: map[memKey]memEntry{}, orders: map[string]Order{}, now: func() time.Time { return time.Now().UTC() }}
}

func clone(o Order) Order {
	o.Items = append([]Item(nil), o.Items...)
	return o
}

func (s *MemoryStore) Create(_ context.Context, key, hash string, o Order) (Order, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := memKey{o.UserID, key}
	if e, ok := s.keys[k]; ok {
		if e.hash != hash {
			return Order{}, false, ErrIdempotencyConflict
		}
		return clone(s.orders[e.orderID]), true, nil
	}
	now := s.now()
	o.Version, o.CreatedAt, o.UpdatedAt = 1, now, now
	if o.Status == "" {
		o.Status = StatusPending
	}
	if o.PaymentStatus == "" {
		o.PaymentStatus = PaymentUnpaid
	}
	s.keys[k] = memEntry{hash: hash, orderID: o.OrderID}
	s.orders[o.OrderID] = clone(o)
	return clone(o), false, nil
}

func (s *MemoryStore) Lookup(_ context.Context, userID, key, hash string) (Order, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.keys[memKey{userID, key}]
	if !ok {
		return Order{}, false, nil
	}
	if e.hash != hash {
		return Order{}, false, ErrIdempotencyConflict
	}
	return clone(s.orders[e.orderID]), true, nil
}

func (s *MemoryStore) Get(_ context.Context, orderID string) (Order, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.orders[orderID]
	if !ok {
		return Order{}, ErrNotFound
	}
	return clone(o), nil
}

func (s *MemoryStore) RecordCapture(_ context.Context, orderID string, capturedVND, refundPendingVND int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.orders[orderID]
	if !ok {
		return ErrNotFound
	}
	o.CapturedVND, o.RefundPendingVND, o.UpdatedAt = capturedVND, refundPendingVND, s.now()
	s.orders[orderID] = o
	return nil
}

func (s *MemoryStore) Transition(_ context.Context, orderID, from, to, paymentStatus, failureCode string) (Order, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.orders[orderID]
	if !ok {
		return Order{}, ErrNotFound
	}
	if o.Status != from {
		return Order{}, ErrStaleState
	}
	o.Status, o.PaymentStatus, o.FailureCode = to, paymentStatus, failureCode
	o.Version++
	o.UpdatedAt = s.now()
	s.orders[orderID] = o
	return clone(o), nil
}
