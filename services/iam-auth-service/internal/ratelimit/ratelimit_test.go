package ratelimit

import (
	"context"
	"testing"
	"time"
)

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time          { return c.now }
func (c *fakeClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

func newTestLimiter() (*Memory, *fakeClock) {
	clock := &fakeClock{now: time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)}
	return NewMemory(5, 15*time.Minute, clock.Now), clock
}

func TestBlocksAfterFiveFailuresInTheWindow(t *testing.T) {
	ctx := context.Background()
	l, _ := newTestLimiter()

	for i := 1; i <= 5; i++ {
		blocked, _, err := l.Blocked(ctx, "k")
		if err != nil || blocked {
			t.Fatalf("attempt %d must be allowed (blocked=%v err=%v)", i, blocked, err)
		}
		if err := l.RecordFailure(ctx, "k"); err != nil {
			t.Fatal(err)
		}
	}
	blocked, retryAfter, err := l.Blocked(ctx, "k")
	if err != nil || !blocked {
		t.Fatalf("the sixth attempt must be blocked (blocked=%v err=%v)", blocked, err)
	}
	if retryAfter <= 0 || retryAfter > 15*time.Minute {
		t.Fatalf("retryAfter = %v, want within the window", retryAfter)
	}
}

func TestWindowExpiresAndCountsRestart(t *testing.T) {
	ctx := context.Background()
	l, clock := newTestLimiter()
	for i := 0; i < 5; i++ {
		_ = l.RecordFailure(ctx, "k")
	}
	if blocked, _, _ := l.Blocked(ctx, "k"); !blocked {
		t.Fatal("must be blocked inside the window")
	}

	clock.Advance(14*time.Minute + 59*time.Second)
	if blocked, _, _ := l.Blocked(ctx, "k"); !blocked {
		t.Fatal("must still be blocked one second before the window ends")
	}
	clock.Advance(2 * time.Second)
	if blocked, _, _ := l.Blocked(ctx, "k"); blocked {
		t.Fatal("must be allowed again after the window ends")
	}
	_ = l.RecordFailure(ctx, "k")
	if blocked, _, _ := l.Blocked(ctx, "k"); blocked {
		t.Fatal("a new window starts with a fresh count")
	}
}

func TestKeysAreIndependentAndResetClearsTheCount(t *testing.T) {
	ctx := context.Background()
	l, _ := newTestLimiter()
	for i := 0; i < 5; i++ {
		_ = l.RecordFailure(ctx, "alice")
	}
	if blocked, _, _ := l.Blocked(ctx, "bob"); blocked {
		t.Fatal("one key must not block another")
	}
	if err := l.Reset(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	if blocked, _, _ := l.Blocked(ctx, "alice"); blocked {
		t.Fatal("a successful sign-in resets the count")
	}
}

func TestFourFailuresDoNotBlock(t *testing.T) {
	ctx := context.Background()
	l, _ := newTestLimiter()
	for i := 0; i < 4; i++ {
		_ = l.RecordFailure(ctx, "k")
	}
	if blocked, _, _ := l.Blocked(ctx, "k"); blocked {
		t.Fatal("four failures must not block")
	}
}

func TestKeyForHidesTheEmailAndSeparatesEmailAndIP(t *testing.T) {
	a := KeyFor("login", "lan@example.com", "10.0.0.1")
	if a == KeyFor("login", "lan@example.com", "10.0.0.2") || a == KeyFor("login", "other@example.com", "10.0.0.1") {
		t.Fatal("different email or ip must give different keys")
	}
	if a != KeyFor("login", "lan@example.com", "10.0.0.1") {
		t.Fatal("keys must be deterministic")
	}
	if want := "lan@example.com"; len(a) == 0 || contains(a, want) || contains(a, "10.0.0.1") {
		t.Fatalf("the key must not contain the email or the ip: %s", a)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
