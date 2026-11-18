package attempt

import (
	"context"
	"sort"

	"github.com/hieuvu0611/commercelab-retail-be/contracts/events"
	"sync"
	"time"
)

type memReservation struct {
	orderID   string
	amountVND int64
	status    string
}

// MemoryStore is the dev/test store used when Postgres is absent (ALLOW_MEMORY_FALLBACK=true).
type MemoryStore struct {
	mu           sync.Mutex
	byID         map[string]Attempt
	byKey        map[string]string // idempotency key -> attempt id
	byOrder      map[string]string // order id -> attempt id
	summaries    map[string]*Summary
	obligations  map[string][]Obligation
	reservations map[string]*memReservation
	events       map[string]bool
	codParcels   map[string]map[string]CODParcel
	outbox       []events.DomainEvent
	now          func() time.Time
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
		summaries: map[string]*Summary{}, obligations: map[string][]Obligation{},
		reservations: map[string]*memReservation{}, events: map[string]bool{}, codParcels: map[string]map[string]CODParcel{},
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
		if st := s.byID[id].Status; st == StatusProcessing || st == StatusUnknown || st == StatusCODPending {
			return Attempt{}, false, ErrActivePayment
		}
		return Attempt{}, false, ErrAttemptExists
	}
	if a.PaymentMethod == "" {
		a.PaymentMethod = "PREPAID"
	}
	if a.Status == "" {
		if a.PaymentMethod == "COD" {
			a.Status = StatusCODPending
		} else {
			a.Status = StatusProcessing
		}
	}
	a.CreatedAt = s.now()
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

func (s *MemoryStore) ApplyCODParcel(_ context.Context, attemptID string, parcel CODParcel) (Attempt, Change, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.byID[attemptID]
	if !ok {
		return Attempt{}, Change{}, ErrNotFound
	}
	parcels := s.codParcels[attemptID]
	recorded := len(parcels)
	var previous *CODParcel
	if stored, found := parcels[parcel.ShipmentID]; found {
		previous = &stored
	} else {
		recorded++
	}
	for _, stored := range parcels {
		if stored.TotalParcels != parcel.TotalParcels {
			return cur, Change{}, ErrIdempotencyConflict
		}
		break
	}
	next, change, err := applyCODParcel(cur, parcel, previous, recorded, s.now())
	if err != nil || change.Replay {
		return next, change, err
	}
	if parcels == nil {
		parcels = make(map[string]CODParcel)
		s.codParcels[attemptID] = parcels
	}
	parcels[parcel.ShipmentID] = parcel
	s.store(cur, next, change)
	return next, change, nil
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

func (s *MemoryStore) ReserveRefund(_ context.Context, orderID, reservationID string, amountVND int64) (Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sum, ok := s.summaries[orderID]
	if !ok {
		return Summary{}, ErrNotFound
	}
	if reservationID != "" {
		if existing, exists := s.reservations[reservationID]; exists {
			if existing.orderID != orderID || existing.amountVND != amountVND {
				return *sum, ErrIdempotencyConflict
			}
			return *sum, nil
		}
	}
	remaining := sum.CapturedVND - sum.RefundedVND - sum.RefundPending
	if amountVND <= 0 || amountVND > remaining {
		return *sum, ErrRefundCeiling
	}
	sum.RefundPending += amountVND
	if reservationID != "" {
		s.reservations[reservationID] = &memReservation{orderID: orderID, amountVND: amountVND, status: ObligationPending}
	}
	return *sum, nil
}

func (s *MemoryStore) CompleteRefundReservation(_ context.Context, orderID, reservationID string, amountVND int64, providerRef string) (Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sum, ok := s.summaries[orderID]
	if !ok {
		return Summary{}, ErrNotFound
	}
	if reservationID != "" {
		if existing, exists := s.reservations[reservationID]; exists {
			if existing.status == ObligationDone {
				return *sum, nil
			}
			if amountVND <= 0 {
				amountVND = existing.amountVND
			}
		}
	}
	if amountVND <= 0 || amountVND > sum.RefundPending || sum.RefundedVND+amountVND > sum.CapturedVND {
		return *sum, ErrRefundCeiling
	}
	sum.RefundPending -= amountVND
	sum.RefundedVND += amountVND
	if reservationID != "" {
		s.reservations[reservationID] = &memReservation{orderID: orderID, amountVND: amountVND, status: ObligationDone}
	}
	at := s.now()
	o := Obligation{OrderID: orderID, AttemptID: "PAY-" + orderID, AmountVND: amountVND, Reason: "RMA_REFUND", Status: ObligationDone, ProviderRef: providerRef, RefundedAt: &at}
	s.outbox = append(s.outbox, RefundedEvent(o, *sum, at))
	return *sum, nil
}

func (s *MemoryStore) Metrics(_ context.Context, since *time.Time) (int64, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var collected, refunded int64
	for _, a := range s.byID {
		if a.Status != StatusCaptured && a.Status != StatusCOLLECTED {
			continue
		}
		ts := a.CreatedAt
		if a.OutcomeAt != nil {
			ts = *a.OutcomeAt
		}
		if since != nil && ts.Before(*since) {
			continue
		}
		amt := a.CapturedAmountVND
		if amt <= 0 {
			amt = a.AmountVND
		}
		collected += amt
	}
	for _, list := range s.obligations {
		for _, o := range list {
			if o.Status == ObligationDone && o.RefundedAt != nil && (since == nil || !o.RefundedAt.Before(*since)) {
				refunded += o.AmountVND
			}
		}
	}
	for _, r := range s.reservations {
		if r.status == ObligationDone {
			refunded += r.amountVND
		}
	}
	return collected, refunded, nil
}
