package repository

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/contracts/events"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/outbox"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/password"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const FinancialRetentionYears = 10

var (
	ErrUserNotFound          = errors.New("user not found")
	ErrInvalidCredentials    = errors.New("email or password is incorrect")
	ErrEmailTaken            = errors.New("email is already registered")
	ErrStaffCannotSelfDelete = errors.New("staff accounts cannot be self-deleted")
	ErrAccountDeleted        = errors.New("account has been deleted")
)

type User struct {
	ID             string      `json:"id"`
	Email          string      `json:"email"`
	Name           string      `json:"name"`
	PasswordHash   string      `json:"-"`
	Role           string      `json:"role"`
	WarehouseScope string      `json:"warehouseScope"`
	Phone          string      `json:"phone"`
	IsActive       bool        `json:"isActive"`
	DeletedAt      *time.Time  `json:"deletedAt,omitempty"`
	AnonymizedAt   *time.Time  `json:"anonymizedAt,omitempty"`
	Grants         []RoleGrant `json:"-"`
}

type AccountDeletionResult struct {
	UserID          string
	AnonymizedEmail string
	ErasedAt        time.Time
	RetentionUntil  time.Time
}

func AnonymizedEmail(userID string) string {
	return "deleted-" + userID + "@anonymized.local"
}

// UserRepository reads users from the iam schema in Postgres. There is no in-memory fallback:
// when Postgres fails, callers receive an error marked with dependency.ErrUnavailable.
type UserRepository struct {
	pool *pgxpool.Pool
}

func NewUserRepository(pool *pgxpool.Pool) *UserRepository {
	return &UserRepository{pool: pool}
}

// decoyHash is verified when the email is unknown so that unknown and wrong-password logins cost the same.
var decoyHash, _ = password.Hash("decoy-password-for-timing")

func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// Authenticate returns the active user whose password matches. It never reveals whether the email exists.
func (r *UserRepository) Authenticate(ctx context.Context, email, plain string) (User, error) {
	u, err := r.FindByEmail(ctx, email)
	if dependency.IsUnavailable(err) {
		return User{}, err
	}
	if err != nil {
		_, _ = password.Verify(plain, decoyHash)
		return User{}, ErrInvalidCredentials
	}
	ok, verr := password.Verify(plain, u.PasswordHash)
	if verr != nil || !ok || !u.IsActive || u.DeletedAt != nil {
		return User{}, ErrInvalidCredentials
	}
	if password.NeedsRehash(u.PasswordHash) {
		r.upgradeHash(ctx, u.ID, plain)
	}
	return u, nil
}

// upgradeHash re-hashes a password with the current parameters. It is best effort: login already succeeded.
func (r *UserRepository) upgradeHash(ctx context.Context, userID, plain string) {
	hash, err := password.Hash(plain)
	if err != nil {
		return
	}
	_, _ = r.pool.Exec(ctx, `UPDATE users SET password_hash = $1 WHERE id = $2`, hash, userID)
}

// Register creates a CUSTOMER account. The caller has already validated and normalized the input.
// It returns ErrEmailTaken when the email exists (emails are unique case-insensitively because they are stored lower-cased).
func (r *UserRepository) Register(ctx context.Context, email, plain, fullName string) (User, error) {
	hash, err := password.Hash(plain)
	if err != nil {
		return User{}, err
	}
	id, err := newUserID()
	if err != nil {
		return User{}, err
	}
	u := User{ID: id, Email: NormalizeEmail(email), Name: fullName, PasswordHash: hash, Role: "CUSTOMER", WarehouseScope: "NONE", IsActive: true,
		Grants: []RoleGrant{{Role: "CUSTOMER"}}}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return User{}, dependency.Wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `
		INSERT INTO users (id, email, full_name, password_hash, role, warehouse_scope, is_active)
		VALUES ($1, $2, $3, $4, $5, $6, TRUE)
	`, u.ID, u.Email, u.Name, u.PasswordHash, u.Role, u.WarehouseScope)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return User{}, ErrEmailTaken
		}
		return User{}, dependency.Wrap(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO user_roles (user_id, role) VALUES ($1, 'CUSTOMER')`, u.ID); err != nil {
		return User{}, dependency.Wrap(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, dependency.Wrap(err)
	}
	return u, nil
}

func newUserID() (string, error) {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "usr-" + hex.EncodeToString(buf), nil
}

const selectUser = `SELECT id, email, full_name, password_hash, role, warehouse_scope, is_active, COALESCE(phone, ''), deleted_at, anonymized_at FROM users`

func (r *UserRepository) FindByEmail(ctx context.Context, email string) (User, error) {
	return r.findOne(ctx, selectUser+` WHERE email = $1`, NormalizeEmail(email))
}

func (r *UserRepository) FindByID(ctx context.Context, id string) (User, error) {
	return r.findOne(ctx, selectUser+` WHERE id = $1`, id)
}

func (r *UserRepository) findOne(ctx context.Context, query string, arg string) (User, error) {
	var u User
	err := r.pool.QueryRow(ctx, query, arg).
		Scan(&u.ID, &u.Email, &u.Name, &u.PasswordHash, &u.Role, &u.WarehouseScope, &u.IsActive, &u.Phone, &u.DeletedAt, &u.AnonymizedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrUserNotFound
	}
	if err != nil {
		return User{}, dependency.Wrap(err)
	}
	if u.Grants, err = r.loadGrants(ctx, u.ID); err != nil {
		return User{}, err
	}
	return u, nil
}

func (r *UserRepository) loadGrants(ctx context.Context, userID string) ([]RoleGrant, error) {
	rows, err := r.pool.Query(ctx, `SELECT role, COALESCE(warehouse_id, '') FROM user_roles WHERE user_id = $1 ORDER BY role, warehouse_id`, userID)
	if err != nil {
		return nil, dependency.Wrap(err)
	}
	defer rows.Close()
	var grants []RoleGrant
	for rows.Next() {
		var g RoleGrant
		if err := rows.Scan(&g.Role, &g.WarehouseID); err != nil {
			return nil, dependency.Wrap(err)
		}
		grants = append(grants, g)
	}
	return grants, dependency.Wrap(rows.Err())
}

// ProfileUpdate holds the profile fields a user may change; nil means "leave as is".
// A pointer to an empty Phone clears the phone number.
type ProfileUpdate struct {
	FullName *string
	Phone    *string
}

// UpdateProfile changes the given fields and returns the updated user.
func (r *UserRepository) UpdateProfile(ctx context.Context, userID string, update ProfileUpdate) (User, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE users
		SET full_name = COALESCE($2, full_name),
		    phone = CASE WHEN $3::boolean THEN NULLIF($4, '') ELSE phone END
		WHERE id = $1 AND is_active = TRUE AND deleted_at IS NULL
	`, userID, update.FullName, update.Phone != nil, deref(update.Phone))
	if err != nil {
		return User{}, dependency.Wrap(err)
	}
	if tag.RowsAffected() == 0 {
		return User{}, ErrUserNotFound
	}
	return r.FindByID(ctx, userID)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (r *UserRepository) ChangePassword(ctx context.Context, userID, current, next string) error {
	u, err := r.FindByID(ctx, userID)
	if err != nil {
		return err
	}
	if !u.IsActive || u.DeletedAt != nil {
		return ErrAccountDeleted
	}
	ok, verr := password.Verify(current, u.PasswordHash)
	if verr != nil || !ok {
		return ErrInvalidCredentials
	}
	hash, err := password.Hash(next)
	if err != nil {
		return err
	}
	if _, err := r.pool.Exec(ctx, `UPDATE users SET password_hash = $2 WHERE id = $1 AND is_active = TRUE AND deleted_at IS NULL`, userID, hash); err != nil {
		return dependency.Wrap(err)
	}
	return nil
}

func (r *UserRepository) DeleteAndAnonymize(ctx context.Context, userID, currentPassword string) (AccountDeletionResult, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return AccountDeletionResult{}, dependency.Wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var (
		passwordHash string
		legacyRole   string
		isActive     bool
		deletedAt    *time.Time
	)
	err = tx.QueryRow(ctx, `
		SELECT password_hash, role, is_active, deleted_at
		FROM iam.users
		WHERE id = $1
		FOR UPDATE
	`, userID).Scan(&passwordHash, &legacyRole, &isActive, &deletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return AccountDeletionResult{}, ErrUserNotFound
	}
	if err != nil {
		return AccountDeletionResult{}, dependency.Wrap(err)
	}
	if !isActive || deletedAt != nil {
		return AccountDeletionResult{}, ErrAccountDeleted
	}

	if legacyRole != "" && legacyRole != "CUSTOMER" {
		return AccountDeletionResult{}, ErrStaffCannotSelfDelete
	}
	rows, err := tx.Query(ctx, `SELECT role FROM iam.user_roles WHERE user_id = $1`, userID)
	if err != nil {
		return AccountDeletionResult{}, dependency.Wrap(err)
	}
	isStaff := false
	for rows.Next() {
		var role string
		if err := rows.Scan(&role); err != nil {
			rows.Close()
			return AccountDeletionResult{}, dependency.Wrap(err)
		}
		if role != "CUSTOMER" {
			isStaff = true
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return AccountDeletionResult{}, dependency.Wrap(err)
	}
	if isStaff {
		return AccountDeletionResult{}, ErrStaffCannotSelfDelete
	}

	ok, verr := password.Verify(currentPassword, passwordHash)
	if verr != nil || !ok {
		return AccountDeletionResult{}, ErrInvalidCredentials
	}

	now := time.Now().UTC()
	retentionUntil := now.AddDate(FinancialRetentionYears, 0, 0)
	anonEmail := AnonymizedEmail(userID)

	if _, err := tx.Exec(ctx, `
		UPDATE iam.users
		SET email = $2,
		    full_name = 'Deleted User',
		    phone = NULL,
		    password_hash = '!DELETED!',
		    is_active = FALSE,
		    deleted_at = $3,
		    anonymized_at = $3
		WHERE id = $1
	`, userID, anonEmail, now); err != nil {
		return AccountDeletionResult{}, dependency.Wrap(err)
	}

	if _, err := tx.Exec(ctx, `DELETE FROM iam.customer_addresses WHERE user_id = $1`, userID); err != nil {
		return AccountDeletionResult{}, dependency.Wrap(err)
	}

	if _, err := tx.Exec(ctx, `UPDATE iam.refresh_tokens SET revoked_at = $2 WHERE user_id = $1 AND revoked_at IS NULL`, userID, now); err != nil {
		return AccountDeletionResult{}, dependency.Wrap(err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO iam.pii_erasure_tombstones (user_id, anonymized_email, erased_at, retention_until)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id) DO NOTHING
	`, userID, anonEmail, now, retentionUntil); err != nil {
		return AccountDeletionResult{}, dependency.Wrap(err)
	}

	detailRaw, err := json.Marshal(map[string]any{
		"retentionUntil": retentionUntil.Format(time.RFC3339),
	})
	if err != nil {
		return AccountDeletionResult{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO iam.auth_audit_logs (event, actor_user_id, target_user_id, outcome, detail, at)
		VALUES ('ACCOUNT_DELETED', $1, $1, 'SUCCESS', $2::jsonb, $3)
	`, userID, string(detailRaw), now); err != nil {
		return AccountDeletionResult{}, dependency.Wrap(err)
	}

	evt := outbox.NewEvent(events.EventUserPIIErased, "User", userID, now, map[string]any{
		"userId":         userID,
		"erasedAt":       now.Format(time.RFC3339),
		"retentionUntil": retentionUntil.Format(time.RFC3339),
	})
	if err := outbox.Insert(ctx, tx, outbox.IAMTable, evt); err != nil {
		return AccountDeletionResult{}, dependency.Wrap(err)
	}

	if err := tx.Commit(ctx); err != nil {
		return AccountDeletionResult{}, dependency.Wrap(err)
	}
	return AccountDeletionResult{
		UserID:          userID,
		AnonymizedEmail: anonEmail,
		ErasedAt:        now,
		RetentionUntil:  retentionUntil,
	}, nil
}
