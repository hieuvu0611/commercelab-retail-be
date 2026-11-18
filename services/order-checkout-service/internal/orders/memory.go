package orders

import (
	"context"
	"github.com/hieuvu0611/commercelab-retail-be/contracts/events"
	"sort"
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
	mu       sync.Mutex
	keys     map[memKey]memEntry
	orders   map[string]Order
	orderSeq map[string]uint64
	seq      uint64
	outbox   []events.DomainEvent
	now      func() time.Time
}

// Outbox returns the events written so far, oldest first (tests).
func (s *MemoryStore) Outbox() []events.DomainEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]events.DomainEvent(nil), s.outbox...)
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		keys:     map[memKey]memEntry{},
		orders:   map[string]Order{},
		orderSeq: map[string]uint64{},
		now:      func() time.Time { return time.Now().UTC() },
	}
}

// WithClock replaces the clock that stamps created and updated times (tests).
func (s *MemoryStore) WithClock(now func() time.Time) *MemoryStore { s.now = now; return s }

func clone(o Order) Order {
	o.Items = append([]Item(nil), o.Items...)
	if len(o.Parcels) > 0 {
		parcels := make([]Parcel, len(o.Parcels))
		for i, p := range o.Parcels {
			p.Items = append([]Item(nil), p.Items...)
			parcels[i] = p
		}
		o.Parcels = parcels
	}
	return o
}

func (s *MemoryStore) Create(_ context.Context, key, hash string, o Order, ev ...events.DomainEvent) (Order, bool, error) {
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
	if o.PaymentMethod == "" {
		o.PaymentMethod = "PREPAID"
	}
	if o.PaymentMethod == "COD" {
		if count := s.countActiveCODLocked(o.UserID); count >= MaxActiveCODOrders {
			return Order{}, false, ErrCODVelocityLimit
		}
	}
	s.seq++
	s.keys[k] = memEntry{hash: hash, orderID: o.OrderID}
	s.orders[o.OrderID] = clone(o)
	s.orderSeq[o.OrderID] = s.seq
	s.outbox = append(s.outbox, ev...)
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

func (s *MemoryStore) Query(_ context.Context, f Filter) ([]Order, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Order{}
	for _, o := range s.orders {
		if matches(o, f) {
			c := clone(o)
			for _, it := range c.Items {
				c.ItemCount += it.Quantity
			}
			c.Items = nil
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		if s.orderSeq[out[i].OrderID] != s.orderSeq[out[j].OrderID] {
			return s.orderSeq[out[i].OrderID] > s.orderSeq[out[j].OrderID]
		}
		return out[i].OrderID > out[j].OrderID
	})
	total := len(out)
	from, to := pageBounds(f, total)
	return out[from:to], total, nil
}

func matches(o Order, f Filter) bool {
	if f.UserID != "" && o.UserID != f.UserID || f.FailureCode != "" && o.FailureCode != f.FailureCode {
		return false
	}
	if !f.UpdatedBefore.IsZero() && o.UpdatedAt.After(f.UpdatedBefore) {
		return false
	}
	if len(f.WarehouseIDs) > 0 {
		matchedWH := contains(f.WarehouseIDs, o.WarehouseID)
		if !matchedWH {
			for _, p := range o.Parcels {
				if contains(f.WarehouseIDs, p.WarehouseID) {
					matchedWH = true
					break
				}
			}
		}
		if !matchedWH {
			return false
		}
	}
	if !f.UpdatedAfter.IsZero() && o.UpdatedAt.Before(f.UpdatedAfter) {
		return false
	}
	if len(f.Statuses) == 0 {
		return true
	}
	for _, st := range f.Statuses {
		if o.Status == st {
			return true
		}
	}
	return false
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func pageBounds(f Filter, total int) (int, int) {
	from, limit := max(f.Offset, 0), PageLimit(f.Limit)
	if from > total {
		from = total
	}
	return from, min(from+limit, total)
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

func (s *MemoryStore) countActiveCODLocked(userID string) int {
	var count int
	for _, o := range s.orders {
		if o.UserID == userID && o.PaymentMethod == "COD" && o.PaymentStatus == "COD_PENDING" &&
			o.Status != "DELIVERED" && o.Status != "CANCELLED" && o.Status != "FAILED_CLOSED" {
			count++
		}
	}
	return count
}

func (s *MemoryStore) CountActiveCODOrders(_ context.Context, userID string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.countActiveCODLocked(userID), nil
}

func (s *MemoryStore) RecordRefund(_ context.Context, orderID string, refundedVND, refundPendingVND int64, paymentStatus string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.orders[orderID]
	if !ok {
		return ErrNotFound
	}
	o.RefundedVND, o.RefundPendingVND, o.PaymentStatus, o.UpdatedAt = refundedVND, refundPendingVND, paymentStatus, s.now()
	s.orders[orderID] = o
	return nil
}

func (s *MemoryStore) Transition(_ context.Context, orderID, from, to, paymentStatus, failureCode string, ev ...events.DomainEvent) (Order, error) {
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
	s.outbox = append(s.outbox, ev...)
	return clone(o), nil
}

// MarkParcelDelivered marks one warehouse's parcel as DELIVERED and returns true when all parcels of the order are delivered.
func (s *MemoryStore) MarkParcelDelivered(_ context.Context, orderID, warehouseID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.orders[orderID]
	if !ok {
		return false, ErrNotFound
	}
	if len(o.Parcels) <= 1 {
		if len(o.Parcels) == 1 {
			o.Parcels[0].Status = "DELIVERED"
			s.orders[orderID] = o
		}
		return true, nil
	}
	all := true
	for i := range o.Parcels {
		if warehouseID != "" && o.Parcels[i].WarehouseID == warehouseID {
			o.Parcels[i].Status = "DELIVERED"
		}
		if o.Parcels[i].Status != "DELIVERED" {
			all = false
		}
	}
	s.orders[orderID] = o
	return all, nil
}
