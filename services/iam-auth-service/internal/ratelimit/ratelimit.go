// Package ratelimit limits failed sign-in attempts with a fixed window per key.
package ratelimit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

const (
	// MaxFailures is the number of failed attempts allowed per key and window (P1-T08).
	MaxFailures = 5
	// Window is the length of the fixed window.
	Window = 15 * time.Minute
)

// Limiter counts failed attempts per key. Only failures count, and a success resets the key.
type Limiter interface {
	// Blocked reports whether the key already used its failures in the current window, and how long until the window ends.
	Blocked(ctx context.Context, key string) (blocked bool, retryAfter time.Duration, err error)
	// RecordFailure counts one failed attempt; the first failure starts the window.
	RecordFailure(ctx context.Context, key string) error
	// Reset forgets the key (after a successful sign-in).
	Reset(ctx context.Context, key string) error
}

// KeyFor builds the limiter key for an email and client address. Only a hash is used, so the key (stored in
// Redis) never contains the email address or the IP.
func KeyFor(scope, email, ip string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email)) + "|" + ip))
	return "rl:" + scope + ":" + hex.EncodeToString(sum[:16])
}

type window struct {
	count   int
	resetAt time.Time
}

// Memory is an in-process Limiter with an injectable clock. It is used by tests; production uses Redis.
type Memory struct {
	max    int
	length time.Duration
	now    func() time.Time
	mu     sync.Mutex
	keys   map[string]*window
}

func NewMemory(max int, length time.Duration, now func() time.Time) *Memory {
	return &Memory{max: max, length: length, now: now, keys: map[string]*window{}}
}

func (m *Memory) current(key string) *window {
	w, ok := m.keys[key]
	if ok && !m.now().Before(w.resetAt) {
		delete(m.keys, key)
		return nil
	}
	return w
}

func (m *Memory) Blocked(_ context.Context, key string) (bool, time.Duration, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	w := m.current(key)
	if w == nil || w.count < m.max {
		return false, 0, nil
	}
	return true, w.resetAt.Sub(m.now()), nil
}

func (m *Memory) RecordFailure(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	w := m.current(key)
	if w == nil {
		w = &window{resetAt: m.now().Add(m.length)}
		m.keys[key] = w
	}
	w.count++
	return nil
}

func (m *Memory) Reset(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.keys, key)
	return nil
}
