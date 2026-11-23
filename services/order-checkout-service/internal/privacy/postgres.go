package privacy

import (
	"context"
	"errors"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

func (s *PostgresStore) Record(ctx context.Context, t Tombstone) error {
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

	_, err := s.pool.Exec(ctx, `
		INSERT INTO orders.pii_erasure_tombstones (user_id, erased_at, retention_until)
		VALUES ($1, $2, $3)
		ON CONFLICT (user_id) DO NOTHING`,
		t.UserID, t.ErasedAt, t.RetentionUntil,
	)
	return dependency.Wrap(err)
}

func (s *PostgresStore) IsErased(ctx context.Context, userID string) (bool, time.Time, error) {
	if userID == "" {
		return false, time.Time{}, nil
	}
	var erasedAt time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT erased_at FROM orders.pii_erasure_tombstones
		WHERE user_id = $1`,
		userID,
	).Scan(&erasedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, time.Time{}, nil
	}
	if err != nil {
		return false, time.Time{}, dependency.Wrap(err)
	}
	return true, erasedAt.UTC(), nil
}
