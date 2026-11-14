//go:build integration

package repository

import (
	"context"
	"errors"
	"testing"
	"time"
)

type refreshFixture struct {
	repo *RefreshRepository
	ctx  context.Context
	now  time.Time
}

func newRefreshFixture(t *testing.T) *refreshFixture {
	t.Helper()
	users, ctx := integrationRepo(t)
	f := &refreshFixture{repo: NewRefreshRepository(users), ctx: ctx, now: time.Now().UTC()}
	f.repo.now = func() time.Time { return f.now }
	return f
}

// issue stores the first token of a new family and returns it with its raw value.
func (f *refreshFixture) issue(t *testing.T, userID string, ttl time.Duration) (RefreshTokenRecord, string) {
	t.Helper()
	id, _ := NewTokenID("rt-")
	family, _ := NewTokenID("fam-")
	raw := "raw-" + id
	rec := RefreshTokenRecord{ID: id, UserID: userID, FamilyID: family, ExpiresAt: f.now.Add(ttl)}
	if err := f.repo.Create(f.ctx, rec, HashToken(raw)); err != nil {
		t.Fatalf("create: %v", err)
	}
	return rec, raw
}

func (f *refreshFixture) rotate(t *testing.T, raw string) (RefreshTokenRecord, string, error) {
	t.Helper()
	id, _ := NewTokenID("rt-")
	nextRaw := "raw-" + id
	next, err := f.repo.Rotate(f.ctx, HashToken(raw), RefreshTokenRecord{ID: id, ExpiresAt: f.now.Add(7 * 24 * time.Hour)}, HashToken(nextRaw))
	return next, nextRaw, err
}

func TestRotationReplacesTheTokenInsideTheSameFamily(t *testing.T) {
	f := newRefreshFixture(t)
	first, firstRaw := f.issue(t, "user-lan", time.Hour)

	second, secondRaw, err := f.rotate(t, firstRaw)
	if err != nil {
		t.Fatalf("first rotation: %v", err)
	}
	if second.FamilyID != first.FamilyID || second.UserID != "user-lan" || second.ID == first.ID {
		t.Fatalf("rotation must stay in the family and use a new id: first=%+v second=%+v", first, second)
	}
	if _, _, err := f.rotate(t, secondRaw); err != nil {
		t.Fatalf("the new token must be usable: %v", err)
	}
}

func TestReusingARotatedTokenRevokesTheWholeFamily(t *testing.T) {
	f := newRefreshFixture(t)
	_, firstRaw := f.issue(t, "user-lan", time.Hour)
	_, secondRaw, err := f.rotate(t, firstRaw)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}

	// An attacker (or a stale client) presents the already-rotated first token.
	if _, _, err := f.rotate(t, firstRaw); !errors.Is(err, ErrRefreshReused) {
		t.Fatalf("reuse err = %v, want ErrRefreshReused", err)
	}
	// The legitimate successor is now dead as well.
	if _, _, err := f.rotate(t, secondRaw); !errors.Is(err, ErrRefreshReused) {
		t.Fatalf("successor after reuse err = %v, want ErrRefreshReused", err)
	}
}

func TestExpiredAndUnknownTokensAreRejected(t *testing.T) {
	f := newRefreshFixture(t)
	_, raw := f.issue(t, "user-lan", time.Hour)

	f.now = f.now.Add(2 * time.Hour)
	if _, _, err := f.rotate(t, raw); !errors.Is(err, ErrRefreshExpired) {
		t.Fatalf("expired err = %v, want ErrRefreshExpired", err)
	}
	if _, _, err := f.rotate(t, "raw-never-issued"); !errors.Is(err, ErrRefreshInvalid) {
		t.Fatalf("unknown err = %v, want ErrRefreshInvalid", err)
	}
}

func TestLogoutRevokesTheFamilyAndIsIdempotent(t *testing.T) {
	f := newRefreshFixture(t)
	_, firstRaw := f.issue(t, "user-lan", time.Hour)
	_, secondRaw, err := f.rotate(t, firstRaw)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}

	if err := f.repo.RevokeFamilyOf(f.ctx, HashToken(secondRaw)); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if _, _, err := f.rotate(t, secondRaw); !errors.Is(err, ErrRefreshReused) {
		t.Fatalf("a logged-out token err = %v, want ErrRefreshReused", err)
	}
	if err := f.repo.RevokeFamilyOf(f.ctx, HashToken("raw-never-issued")); err != nil {
		t.Fatalf("logout with an unknown token must not fail: %v", err)
	}
}

func TestRevokeAllForUserKillsEveryFamilyOfThatUserOnly(t *testing.T) {
	f := newRefreshFixture(t)
	_, mine1 := f.issue(t, "user-lan", time.Hour)
	_, mine2 := f.issue(t, "user-lan", time.Hour)
	_, theirs := f.issue(t, "staff-cskh", time.Hour)

	if err := f.repo.RevokeAllForUser(f.ctx, "user-lan"); err != nil {
		t.Fatalf("revoke all: %v", err)
	}
	for _, raw := range []string{mine1, mine2} {
		if _, _, err := f.rotate(t, raw); !errors.Is(err, ErrRefreshReused) {
			t.Fatalf("revoked token err = %v, want ErrRefreshReused", err)
		}
	}
	if _, _, err := f.rotate(t, theirs); err != nil {
		t.Fatalf("another user's token must be unaffected: %v", err)
	}
}

func TestConcurrentRefreshOfOneTokenSucceedsOnlyOnce(t *testing.T) {
	f := newRefreshFixture(t)
	_, raw := f.issue(t, "user-lan", time.Hour)

	const attempts = 6
	results := make(chan error, attempts)
	for i := 0; i < attempts; i++ {
		go func() {
			_, _, err := f.rotate(t, raw)
			results <- err
		}()
	}
	succeeded, reused := 0, 0
	for i := 0; i < attempts; i++ {
		switch err := <-results; {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrRefreshReused):
			reused++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if succeeded != 1 || reused != attempts-1 {
		t.Fatalf("succeeded %d, reused %d; want exactly 1 and %d", succeeded, reused, attempts-1)
	}
}
