package domain

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// InboxStore persists notification items and deduplication keys.
type InboxStore interface {
	InsertIdempotent(ctx context.Context, n NotificationItem) (NotificationItem, bool, error)
	ListInbox(ctx context.Context, recipientID string) ([]NotificationItem, error)
	UnreadCount(ctx context.Context, recipientID string) (int, error)
	MarkAsRead(ctx context.Context, recipientID, notificationID string) (bool, error)
	MarkAllAsRead(ctx context.Context, recipientID string) (int, error)
	EraseUser(ctx context.Context, userID string, erasedAt, retentionUntil time.Time) error
	IsUserErased(ctx context.Context, userID string) (bool, error)
}

// PostgresStore persists notifications in the `notifications` schema.
type PostgresStore struct {
	pool *pgxpool.Pool
}

// NewPostgresStore creates a Postgres-backed notification store.
func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

func (s *PostgresStore) InsertIdempotent(ctx context.Context, n NotificationItem) (NotificationItem, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return n, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var erased bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM notifications.pii_erasure_tombstones WHERE user_id = $1)`,
		n.RecipientID,
	).Scan(&erased); err != nil {
		return n, false, err
	}
	if erased {
		if err := tx.Commit(ctx); err != nil {
			return n, false, err
		}
		return n, true, nil
	}

	tag, err := tx.Exec(ctx, `
		INSERT INTO notifications.inbox_dedup_deliveries (event_id, recipient_id, channel, delivered_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (event_id, recipient_id, channel) DO NOTHING`,
		n.EventID, n.RecipientID, n.Channel, n.CreatedAt,
	)
	if err != nil {
		return n, false, err
	}
	if tag.RowsAffected() == 0 {
		return n, true, nil
	}

	metaJSON, err := json.Marshal(n.Metadata)
	if err != nil || len(metaJSON) == 0 || string(metaJSON) == "null" {
		metaJSON = []byte("{}")
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO notifications.inbox_items (
			notification_id, event_id, recipient_id, channel, category,
			title, message, is_read, metadata_json, created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb,$10)
		ON CONFLICT (event_id, recipient_id, channel) DO NOTHING`,
		n.NotificationID, n.EventID, n.RecipientID, n.Channel, n.Category,
		n.Title, n.Message, n.IsRead, string(metaJSON), n.CreatedAt,
	)
	if err != nil {
		return n, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return n, false, err
	}
	return n, false, nil
}

func (s *PostgresStore) ListInbox(ctx context.Context, recipientID string) ([]NotificationItem, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT notification_id, event_id, recipient_id, channel, category,
		       title, message, is_read, metadata_json, created_at
		FROM notifications.inbox_items
		WHERE recipient_id = $1
		ORDER BY created_at DESC
		LIMIT 50`,
		recipientID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []NotificationItem
	for rows.Next() {
		var it NotificationItem
		var metaRaw []byte
		if err := rows.Scan(
			&it.NotificationID, &it.EventID, &it.RecipientID, &it.Channel, &it.Category,
			&it.Title, &it.Message, &it.IsRead, &metaRaw, &it.CreatedAt,
		); err != nil {
			return nil, err
		}
		if len(metaRaw) > 0 && string(metaRaw) != "{}" {
			_ = json.Unmarshal(metaRaw, &it.Metadata)
		}
		it.CreatedAt = it.CreatedAt.UTC()
		items = append(items, it)
	}
	if items == nil {
		items = []NotificationItem{}
	}
	return items, rows.Err()
}

func (s *PostgresStore) UnreadCount(ctx context.Context, recipientID string) (int, error) {
	var count int
	err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM notifications.inbox_items
		WHERE recipient_id = $1 AND is_read = FALSE`,
		recipientID,
	).Scan(&count)
	return count, err
}

func (s *PostgresStore) MarkAsRead(ctx context.Context, recipientID, notificationID string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE notifications.inbox_items
		SET is_read = TRUE
		WHERE recipient_id = $1 AND notification_id = $2`,
		recipientID, notificationID,
	)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

func (s *PostgresStore) MarkAllAsRead(ctx context.Context, recipientID string) (int, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE notifications.inbox_items
		SET is_read = TRUE
		WHERE recipient_id = $1 AND is_read = FALSE`,
		recipientID,
	)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func (s *PostgresStore) EraseUser(ctx context.Context, userID string, erasedAt, retentionUntil time.Time) error {
	if userID == "" {
		return nil
	}
	if erasedAt.IsZero() {
		erasedAt = time.Now().UTC()
	} else {
		erasedAt = erasedAt.UTC()
	}
	if retentionUntil.IsZero() {
		retentionUntil = erasedAt.AddDate(10, 0, 0)
	} else {
		retentionUntil = retentionUntil.UTC()
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		INSERT INTO notifications.pii_erasure_tombstones (user_id, erased_at, retention_until)
		VALUES ($1, $2, $3)
		ON CONFLICT (user_id) DO NOTHING`,
		userID, erasedAt, retentionUntil,
	); err != nil {
		return err
	}

	if _, err := tx.Exec(ctx, `
		DELETE FROM notifications.inbox_items
		WHERE recipient_id = $1`,
		userID,
	); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func (s *PostgresStore) IsUserErased(ctx context.Context, userID string) (bool, error) {
	if userID == "" {
		return false, nil
	}
	var erased bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM notifications.pii_erasure_tombstones WHERE user_id = $1)`,
		userID,
	).Scan(&erased)
	return erased, err
}

// ErrStoreUnavailable indicates no backing store operation succeeded.
var ErrStoreUnavailable = errors.New("notification store unavailable")

func normalizeItem(n NotificationItem) NotificationItem {
	if n.Channel == "" {
		n.Channel = "IN_APP"
	}
	if n.NotificationID == "" {
		n.NotificationID = "NOTI-" + time.Now().UTC().Format("20060102150405") + "-" + n.EventID
	}
	if n.CreatedAt.IsZero() {
		n.CreatedAt = time.Now().UTC()
	}
	return n
}
