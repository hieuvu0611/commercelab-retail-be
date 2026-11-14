package repository

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/password"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrUserNotFound       = errors.New("user not found")
	ErrInvalidCredentials = errors.New("email or password is incorrect")
	ErrEmailTaken         = errors.New("email is already registered")
)

type User struct {
	ID             string      `json:"id"`
	Email          string      `json:"email"`
	Name           string      `json:"name"`
	PasswordHash   string      `json:"-"`
	Role           string      `json:"role"`
	WarehouseScope string      `json:"warehouseScope"`
	IsActive       bool        `json:"isActive"`
	Grants         []RoleGrant `json:"-"`
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
	if verr != nil || !ok || !u.IsActive {
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

const selectUser = `SELECT id, email, full_name, password_hash, role, warehouse_scope, is_active FROM users`

func (r *UserRepository) FindByEmail(ctx context.Context, email string) (User, error) {
	return r.findOne(ctx, selectUser+` WHERE email = $1`, NormalizeEmail(email))
}

func (r *UserRepository) FindByID(ctx context.Context, id string) (User, error) {
	return r.findOne(ctx, selectUser+` WHERE id = $1`, id)
}

func (r *UserRepository) findOne(ctx context.Context, query string, arg string) (User, error) {
	var u User
	err := r.pool.QueryRow(ctx, query, arg).
		Scan(&u.ID, &u.Email, &u.Name, &u.PasswordHash, &u.Role, &u.WarehouseScope, &u.IsActive)
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
