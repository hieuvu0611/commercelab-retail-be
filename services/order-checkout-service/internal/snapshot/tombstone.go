package snapshot

import (
	"context"
	"time"
)

type ErasureChecker interface {
	IsErased(ctx context.Context, userID string) (bool, time.Time, error)
}

type RedactingStore struct {
	inner   Store
	checker ErasureChecker
}

func WithErasureTombstones(inner Store, checker ErasureChecker) Store {
	if inner == nil || checker == nil {
		return inner
	}
	return &RedactingStore{inner: inner, checker: checker}
}

func (r *RedactingStore) Save(ctx context.Context, s Snapshot) error {
	if s.UserID != "" {
		erased, erasedAt, err := r.checker.IsErased(ctx, s.UserID)
		if err != nil {
			return err
		}
		if erased {
			ts := erasedAt.UTC()
			if erasedAt.IsZero() {
				ts = time.Now().UTC()
			}
			s.ShippingAddress = RedactedAddress()
			s.PIIRedacted = true
			s.PIIRedactedAt = &ts
		}
	}
	return r.inner.Save(ctx, s)
}

func (r *RedactingStore) Get(ctx context.Context, orderID string) (Snapshot, bool, error) {
	s, found, err := r.inner.Get(ctx, orderID)
	if err != nil || !found {
		return s, found, err
	}
	if !s.PIIRedacted && s.UserID != "" {
		erased, erasedAt, err := r.checker.IsErased(ctx, s.UserID)
		if err != nil {
			return Snapshot{}, false, err
		}
		if erased {
			ts := erasedAt.UTC()
			if erasedAt.IsZero() {
				ts = time.Now().UTC()
			}
			s.ShippingAddress = RedactedAddress()
			s.PIIRedacted = true
			s.PIIRedactedAt = &ts
		}
	}
	return s, true, nil
}

func (r *RedactingStore) RedactUserPII(ctx context.Context, userID string, erasedAt time.Time) (int64, error) {
	return r.inner.RedactUserPII(ctx, userID, erasedAt)
}
