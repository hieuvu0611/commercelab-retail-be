package attempt

import (
	"context"
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
	now         func() time.Time
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
	s.byID[attemptID] = next
	sum := s.summaries[cur.OrderID]
	sum.CapturedVND += ch.CapturedVND
	if ch.LateRefundVND > 0 {
		sum.RefundPending += ch.LateRefundVND
		s.obligations[cur.OrderID] = append(s.obligations[cur.OrderID],
			Obligation{OrderID: cur.OrderID, AttemptID: attemptID, AmountVND: ch.LateRefundVND, Reason: "LATE_CAPTURE", Status: "PENDING"})
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
