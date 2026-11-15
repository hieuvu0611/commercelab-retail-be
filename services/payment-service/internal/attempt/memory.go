package attempt

import (
	"context"
	"sort"

	"github.com/hieuvu0611/commercelab-retail-be/contracts/events"
	"sync"
	"time"
)

// MemoryStore is the dev/test store used when Postgres is absent (ALLOW_MEMORY_FALLBACK=true).
type MemoryStore struct {
	mu          sync.Mutex
	byID        map[string]Attempt
	byKey       map[string]string // idempotency key -> attempt id
	byOrder     map[string]string // order id -> attempt id
	summaries   map[string]*Summary
	obligations map[string][]Obligation
	events      map[string]bool
	outbox      []events.DomainEvent
	now         func() time.Time
}

// Outbox returns the events written so far, oldest first (tests).
func (s *MemoryStore) Outbox() []events.DomainEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]events.DomainEvent(nil), s.outbox...)
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		byID: map[string]Attempt{}, byKey: map[string]string{}, byOrder: map[string]string{},
		summaries: map[string]*Summary{}, obligations: map[string][]Obligation{}, events: map[string]bool{},
		now: func() time.Time { return time.Now().UTC() },
	}
}

// WithClock replaces the clock (tests).
func (s *MemoryStore) WithClock(now func() time.Time) *MemoryStore { s.now = now; return s }

func (s *MemoryStore) Create(_ context.Context, a Attempt) (Attempt, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.byKey[a.IdempotencyKey]; ok {
		cur := s.byID[id]
		if cur.OrderID == a.OrderID && cur.UserID == a.UserID && cur.AmountVND == a.AmountVND {
			return cur, true, nil
		}
		return Attempt{}, false, ErrIdempotencyConflict
	}
	if id, ok := s.byOrder[a.OrderID]; ok {
		if st := s.byID[id].Status; st == StatusProcessing || st == StatusUnknown {
			return Attempt{}, false, ErrActivePayment
		}
		return Attempt{}, false, ErrAttemptExists
	}
	a.Status, a.CreatedAt = StatusProcessing, s.now()
	s.byID[a.AttemptID], s.byKey[a.IdempotencyKey], s.byOrder[a.OrderID] = a, a.AttemptID, a.AttemptID
	s.summaries[a.OrderID] = &Summary{OrderID: a.OrderID, ExpectedVND: a.AmountVND}
	return a, false, nil
}

func (s *MemoryStore) Get(_ context.Context, attemptID string) (Attempt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.byID[attemptID]
	if !ok {
		return Attempt{}, ErrNotFound
	}
	return a, nil
}

func (s *MemoryStore) GetByOrder(_ context.Context, orderID string) (Attempt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.byOrder[orderID]
	if !ok {
		return Attempt{}, ErrNotFound
	}
	return s.byID[id], nil
}

func (s *MemoryStore) Apply(_ context.Context, attemptID string, o Outcome, amountVND int64, eventID string) (Attempt, Change, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.byID[attemptID]
	if !ok {
		return Attempt{}, Change{}, ErrNotFound
	}
	if eventID != "" && s.events[eventID] {
		return cur, Change{Replay: true}, nil
	}
	next, ch, err := Apply(cur, o, amountVND, s.now())
	if err != nil {
		return cur, Change{}, err
	}
	if eventID != "" {
		s.events[eventID] = true
	}
	s.store(cur, next, ch)
	return next, ch, nil
}

// store saves the new attempt and its money effects. The caller holds the lock.
func (s *MemoryStore) store(cur, next Attempt, ch Change) {
	s.outbox = append(s.outbox, EventsFor(cur, next, ch, s.now())...)
	s.byID[cur.AttemptID] = next
	sum := s.summaries[cur.OrderID]
	sum.CapturedVND += ch.CapturedVND
	if ch.LateRefundVND > 0 {
		reason := ch.RefundReason
		if reason == "" {
			reason = ReasonLateCapture
		}
		if !s.hasObligation(cur.OrderID, cur.AttemptID) { // one obligation per attempt, however often it is closed
			sum.RefundPending += ch.LateRefundVND
			s.obligations[cur.OrderID] = append(s.obligations[cur.OrderID],
				Obligation{OrderID: cur.OrderID, AttemptID: cur.AttemptID, AmountVND: ch.LateRefundVND, Reason: reason, Status: ObligationPending})
		}
	}
}

func (s *MemoryStore) hasObligation(orderID, attemptID string) bool {
	for _, o := range s.obligations[orderID] {
		if o.AttemptID == attemptID {
			return true
		}
	}
	return false
}

func (s *MemoryStore) Close(ctx context.Context, attemptID string) (Attempt, Change, error) {
	return s.CloseFor(ctx, attemptID, ReasonLateCapture)
}

func (s *MemoryStore) CloseFor(_ context.Context, attemptID, reason string) (Attempt, Change, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.byID[attemptID]
	if !ok {
		return Attempt{}, Change{}, ErrNotFound
	}
	next, ch, err := CloseFor(cur, s.now(), reason)
	if err != nil {
		return cur, Change{}, err
	}
	if !ch.Replay {
		s.store(cur, next, ch)
	}
	return next, ch, nil
}

func (s *MemoryStore) Summary(_ context.Context, orderID string) (Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sum, ok := s.summaries[orderID]
	if !ok {
		return Summary{}, ErrNotFound
	}
	return *sum, nil
}

func (s *MemoryStore) Obligations(_ context.Context, orderID string) ([]Obligation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Obligation(nil), s.obligations[orderID]...), nil
}

func (s *MemoryStore) PendingRefunds(_ context.Context, limit int) ([]Obligation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Obligation
	for _, list := range s.obligations {
		for _, o := range list {
			if o.Status == ObligationPending {
				out = append(out, o)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AttemptID < out[j].AttemptID })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *MemoryStore) CompleteRefund(_ context.Context, attemptID, providerRef string) (Obligation, Summary, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.byID[attemptID]
	if !ok {
		return Obligation{}, Summary{}, false, ErrNotFound
	}
	list := s.obligations[a.OrderID]
	for i, o := range list {
		if o.AttemptID != attemptID {
			continue
		}
		sum := s.summaries[a.OrderID]
		if o.Status == ObligationDone {
			return o, *sum, false, nil
		}
		if sum.RefundedVND+o.AmountVND > sum.CapturedVND || o.AmountVND > sum.RefundPending {
			return Obligation{}, *sum, false, ErrRefundCeiling
		}
		at := s.now()
		o.Status, o.ProviderRef, o.RefundedAt = ObligationDone, providerRef, &at
		list[i] = o
		sum.RefundPending -= o.AmountVND
		sum.RefundedVND += o.AmountVND
		s.outbox = append(s.outbox, RefundedEvent(o, *sum, at))
		return o, *sum, true, nil
	}
	return Obligation{}, Summary{}, false, ErrNotFound
}
