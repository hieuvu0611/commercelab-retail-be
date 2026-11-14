package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/jackc/pgx/v5"
)

var (
	// ErrRefreshInvalid means the token is unknown to the server.
	ErrRefreshInvalid = errors.New("refresh token is invalid")
	// ErrRefreshExpired means the token is past its expiry.
	ErrRefreshExpired = errors.New("refresh token has expired")
	// ErrRefreshReused means an already-rotated or revoked token was presented again; its whole family is revoked.
	ErrRefreshReused = errors.New("refresh token was already used")
)

// HashToken returns the hex SHA-256 of a refresh token; only this value is stored.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// NewTokenID returns a random identifier for a token or a token family.
func NewTokenID(prefix string) (string, error) {
	id, err := newUserID() // 24 random hex characters
	if err != nil {
		return "", err
	}
	return prefix + id[len("usr-"):], nil
}

// RefreshTokenRecord is what the server remembers about a refresh token.
type RefreshTokenRecord struct {
	ID        string
	UserID    string
	FamilyID  string
	ExpiresAt time.Time
}

// RefreshRepository stores hashed refresh tokens in iam.refresh_tokens.
type RefreshRepository struct {
	users *UserRepository
	now   func() time.Time
}

func NewRefreshRepository(users *UserRepository) *RefreshRepository {
	return &RefreshRepository{users: users, now: time.Now}
}

// Create stores a token that starts a new family.
func (r *RefreshRepository) Create(ctx context.Context, rec RefreshTokenRecord, tokenHash string) error {
	_, err := r.users.pool.Exec(ctx, `
		INSERT INTO refresh_tokens (id, user_id, family_id, token_hash, expires_at)
		VALUES ($1, $2, $3, $4, $5)
	`, rec.ID, rec.UserID, rec.FamilyID, tokenHash, rec.ExpiresAt)
	return dependency.Wrap(err)
}

// Rotate atomically retires the presented token and stores its successor in the same family.
//
//   - unknown token: ErrRefreshInvalid
//   - token already rotated or revoked: the whole family is revoked and ErrRefreshReused is returned
//   - expired token: ErrRefreshExpired
//
// Concurrent refreshes of one token are serialized by a row lock, so only the first succeeds.
func (r *RefreshRepository) Rotate(ctx context.Context, presentedHash string, next RefreshTokenRecord, nextHash string) (RefreshTokenRecord, error) {
	tx, err := r.users.pool.Begin(ctx)
	if err != nil {
		return RefreshTokenRecord{}, dependency.Wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var current RefreshTokenRecord
	var revokedAt *time.Time
	err = tx.QueryRow(ctx, `
		SELECT id, user_id, family_id, expires_at, revoked_at
		FROM refresh_tokens WHERE token_hash = $1 FOR UPDATE
	`, presentedHash).Scan(&current.ID, &current.UserID, &current.FamilyID, &current.ExpiresAt, &revokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return RefreshTokenRecord{}, ErrRefreshInvalid
	}
	if err != nil {
		return RefreshTokenRecord{}, dependency.Wrap(err)
	}

	now := r.now().UTC()
	if revokedAt != nil {
		if _, err := tx.Exec(ctx, `UPDATE refresh_tokens SET revoked_at = $2 WHERE family_id = $1 AND revoked_at IS NULL`,
			current.FamilyID, now); err != nil {
			return RefreshTokenRecord{}, dependency.Wrap(err)
		}
		if err := tx.Commit(ctx); err != nil {
			return RefreshTokenRecord{}, dependency.Wrap(err)
		}
		return RefreshTokenRecord{}, ErrRefreshReused
	}
	if !current.ExpiresAt.After(now) {
		return RefreshTokenRecord{}, ErrRefreshExpired
	}

	if _, err := tx.Exec(ctx, `UPDATE refresh_tokens SET revoked_at = $2, replaced_by = $3 WHERE id = $1`,
		current.ID, now, next.ID); err != nil {
		return RefreshTokenRecord{}, dependency.Wrap(err)
	}
	next.UserID, next.FamilyID = current.UserID, current.FamilyID
	if _, err := tx.Exec(ctx, `
		INSERT INTO refresh_tokens (id, user_id, family_id, token_hash, expires_at) VALUES ($1, $2, $3, $4, $5)
	`, next.ID, next.UserID, next.FamilyID, nextHash, next.ExpiresAt); err != nil {
		return RefreshTokenRecord{}, dependency.Wrap(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return RefreshTokenRecord{}, dependency.Wrap(err)
	}
	return next, nil
}

// RevokeFamilyOf revokes every live token in the family of the presented token (logout).
// An unknown token is not an error: logout is idempotent.
func (r *RefreshRepository) RevokeFamilyOf(ctx context.Context, tokenHash string) error {
	_, err := r.users.pool.Exec(ctx, `
		UPDATE refresh_tokens SET revoked_at = $2
		WHERE revoked_at IS NULL AND family_id = (SELECT family_id FROM refresh_tokens WHERE token_hash = $1)
	`, tokenHash, r.now().UTC())
	return dependency.Wrap(err)
}

// RevokeAllForUser revokes every live token of a user, for example after a password change.
func (r *RefreshRepository) RevokeAllForUser(ctx context.Context, userID string) error {
	_, err := r.users.pool.Exec(ctx, `UPDATE refresh_tokens SET revoked_at = $2 WHERE user_id = $1 AND revoked_at IS NULL`,
		userID, r.now().UTC())
	return dependency.Wrap(err)
}
