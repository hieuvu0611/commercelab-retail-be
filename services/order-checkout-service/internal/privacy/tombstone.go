package privacy

import (
	"context"
	"sync"
	"time"
)

type Tombstone struct {
	UserID         string
	ErasedAt       time.Time
	RetentionUntil time.Time
}

type Store interface {
	Record(ctx context.Context, t Tombstone) error
	IsErased(ctx context.Context, userID string) (bool, time.Time, error)
}

type MemoryStore struct {
	mu    sync.RWMutex
	items map[string]Tombstone
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{items: make(map[string]Tombstone)}
}

func (m *MemoryStore) Record(_ context.Context, t Tombstone) error {
	if t.UserID == "" {
		return nil
	}
	if t.ErasedAt.IsZero() {
		t.ErasedAt = time.Now().UTC()
	} else {
		t.ErasedAt = t.ErasedAt.UTC()
	}
	if t.RetentionUntil.IsZero() {
		t.RetentionUntil = t.ErasedAt.AddDate(10, 0, 0)
	} else {
		t.RetentionUntil = t.RetentionUntil.UTC()
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.items[t.UserID]; !exists {
		m.items[t.UserID] = t
	}
	return nil
}

func (m *MemoryStore) IsErased(_ context.Context, userID string) (bool, time.Time, error) {
	if userID == "" {
		return false, time.Time{}, nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.items[userID]
	if !ok {
		return false, time.Time{}, nil
	}
	return true, t.ErasedAt, nil
}
