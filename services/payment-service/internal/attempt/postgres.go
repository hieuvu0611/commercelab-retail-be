package attempt

import (
	"context"
	"errors"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresStore keeps attempts in payments.payment_attempts and the totals in payments.order_payment_summary.
type PostgresStore struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool, now: func() time.Time { return time.Now().UTC() }}
}

const attemptColumns = `attempt_id, order_id, user_id, idempotency_key, amount_vnd, captured_amount_vnd, status, unknown_deadline, outcome_at, created_at`

func scanAttempt(row pgx.Row) (Attempt, error) {
	var a Attempt
	err := row.Scan(&a.AttemptID, &a.OrderID, &a.UserID, &a.IdempotencyKey, &a.AmountVND, &a.CapturedAmountVND, &a.Status, &a.UnknownDeadline, &a.OutcomeAt, &a.CreatedAt)
	return a, err
}

func (s *PostgresStore) Create(ctx context.Context, a Attempt) (Attempt, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Attempt{}, false, dependency.Wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// ON CONFLICT DO NOTHING covers every unique rule at once (key, attempt id, one attempt per order); a
	// concurrent duplicate waits for the first insert and then falls through to the checks below.
	tag, err := tx.Exec(ctx, `INSERT INTO payments.payment_attempts (attempt_id, order_id, user_id, idempotency_key, amount_vnd, status, created_at)
		VALUES ($1, $2, $3, $4, $5, 'PROCESSING', $6) ON CONFLICT DO NOTHING`,
		a.AttemptID, a.OrderID, a.UserID, a.IdempotencyKey, a.AmountVND, s.now())
	if err != nil {
		return Attempt{}, false, dependency.Wrap(err)
	}
	if tag.RowsAffected() == 1 {
		if _, err := tx.Exec(ctx, `INSERT INTO payments.order_payment_summary (order_id, expected_amount) VALUES ($1, $2)
			ON CONFLICT (order_id) DO UPDATE SET expected_amount = EXCLUDED.expected_amount`, a.OrderID, a.AmountVND); err != nil {
			return Attempt{}, false, dependency.Wrap(err)
		}
		stored, err := scanAttempt(tx.QueryRow(ctx, `SELECT `+attemptColumns+` FROM payments.payment_attempts WHERE attempt_id = $1`, a.AttemptID))
		if err != nil {
			return Attempt{}, false, dependency.Wrap(err)
		}
		return stored, false, dependency.Wrap(tx.Commit(ctx))
	}

	if cur, err := scanAttempt(tx.QueryRow(ctx, `SELECT `+attemptColumns+` FROM payments.payment_attempts WHERE idempotency_key = $1`, a.IdempotencyKey)); err == nil {
		if cur.OrderID == a.OrderID && cur.UserID == a.UserID && cur.AmountVND == a.AmountVND {
			return cur, true, nil
		}
		return Attempt{}, false, ErrIdempotencyConflict
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return Attempt{}, false, dependency.Wrap(err)
	}
	cur, err := scanAttempt(tx.QueryRow(ctx, `SELECT `+attemptColumns+` FROM payments.payment_attempts WHERE order_id = $1`, a.OrderID))
	if err != nil {
		return Attempt{}, false, dependency.Wrap(err)
	}
	if cur.Status == StatusProcessing || cur.Status == StatusUnknown {
		return Attempt{}, false, ErrActivePayment
	}
	return Attempt{}, false, ErrAttemptExists
}

func (s *PostgresStore) Get(ctx context.Context, attemptID string) (Attempt, error) {
	a, err := scanAttempt(s.pool.QueryRow(ctx, `SELECT `+attemptColumns+` FROM payments.payment_attempts WHERE attempt_id = $1`, attemptID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Attempt{}, ErrNotFound
	}
	return a, dependency.Wrap(err)
}

func (s *PostgresStore) Apply(ctx context.Context, attemptID string, o Outcome, amountVND int64, eventID string) (Attempt, Change, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Attempt{}, Change{}, dependency.Wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	cur, err := scanAttempt(tx.QueryRow(ctx, `SELECT `+attemptColumns+` FROM payments.payment_attempts WHERE attempt_id = $1 FOR UPDATE`, attemptID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Attempt{}, Change{}, ErrNotFound
	}
	if err != nil {
		return Attempt{}, Change{}, dependency.Wrap(err)
	}
	// The row lock above serialises every outcome of one attempt, so the event check below cannot race.
	if eventID != "" {
		tag, err := tx.Exec(ctx, `INSERT INTO payments.webhook_events (event_id, attempt_id, outcome) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
			eventID, attemptID, string(o))
		if err != nil {
			return Attempt{}, Change{}, dependency.Wrap(err)
		}
		if tag.RowsAffected() == 0 {
			return cur, Change{Replay: true}, nil
		}
	}
	next, ch, err := Apply(cur, o, amountVND, s.now())
	if err != nil {
		return cur, Change{}, err
	}
	if ch.Replay {
		return next, ch, dependency.Wrap(tx.Commit(ctx)) // keeps the event id recorded
	}
	if _, err := tx.Exec(ctx, `UPDATE payments.payment_attempts SET status = $2, captured_amount_vnd = $3, unknown_deadline = $4, outcome_at = $5
		WHERE attempt_id = $1`, attemptID, next.Status, next.CapturedAmountVND, next.UnknownDeadline, next.OutcomeAt); err != nil {
		return Attempt{}, Change{}, dependency.Wrap(err)
	}
	if ch.CapturedVND > 0 {
		if _, err := tx.Exec(ctx, `UPDATE payments.order_payment_summary SET captured_amount = captured_amount + $2, refund_pending = refund_pending + $3,
			version = version + 1 WHERE order_id = $1`, cur.OrderID, ch.CapturedVND, ch.LateRefundVND); err != nil {
			return Attempt{}, Change{}, dependency.Wrap(err)
		}
	}
	if ch.LateRefundVND > 0 {
		if _, err := tx.Exec(ctx, `INSERT INTO payments.refund_obligations (order_id, attempt_id, amount_vnd, reason) VALUES ($1, $2, $3, 'LATE_CAPTURE')
			ON CONFLICT (attempt_id) DO NOTHING`, cur.OrderID, attemptID, ch.LateRefundVND); err != nil {
			return Attempt{}, Change{}, dependency.Wrap(err)
		}
	}
	return next, ch, dependency.Wrap(tx.Commit(ctx))
}

func (s *PostgresStore) Summary(ctx context.Context, orderID string) (Summary, error) {
	var sum Summary
	err := s.pool.QueryRow(ctx, `SELECT order_id, expected_amount, captured_amount, refund_pending FROM payments.order_payment_summary WHERE order_id = $1`, orderID).
		Scan(&sum.OrderID, &sum.ExpectedVND, &sum.CapturedVND, &sum.RefundPending)
	if errors.Is(err, pgx.ErrNoRows) {
		return Summary{}, ErrNotFound
	}
	return sum, dependency.Wrap(err)
}

func (s *PostgresStore) Obligations(ctx context.Context, orderID string) ([]Obligation, error) {
	rows, err := s.pool.Query(ctx, `SELECT order_id, attempt_id, amount_vnd, reason, status FROM payments.refund_obligations WHERE order_id = $1 ORDER BY id`, orderID)
	if err != nil {
		return nil, dependency.Wrap(err)
	}
	defer rows.Close()
	var out []Obligation
	for rows.Next() {
		var o Obligation
		if err := rows.Scan(&o.OrderID, &o.AttemptID, &o.AmountVND, &o.Reason, &o.Status); err != nil {
			return nil, dependency.Wrap(err)
		}
		out = append(out, o)
	}
	return out, dependency.Wrap(rows.Err())
}
