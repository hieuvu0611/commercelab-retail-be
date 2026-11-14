package repository

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/password"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	// ErrSelfModification means an administrator tried to change their own access (B09: no self-grant).
	ErrSelfModification = errors.New("administrators cannot change their own access")
	// ErrUnknownRole means a grant names a role that does not exist.
	ErrUnknownRole = errors.New("unknown role")
)

// KnownRoles are the roles of the business blueprint (iam.roles).
var KnownRoles = []string{
	"CUSTOMER", "PRODUCT_STAFF", "MARKETING", "PROCUREMENT", "WAREHOUSE_STAFF", "WAREHOUSE_MGR", "DELIVERY_COORD",
	"SUPPORT_AGENT", "SUPPORT_LEAD", "FINANCE", "FINANCE_MGR", "OPS_MGR", "ACCESS_ADMIN",
}

// IsWarehouseRole reports whether grants of role carry a warehouse.
func IsWarehouseRole(role string) bool { return warehouseRoles[role] }

// AdminRepository performs staff administration. Every change writes an audit record with the actor and the
// reason inside the same transaction, so a change without a trace cannot happen.
type AdminRepository struct {
	users *UserRepository
	now   func() time.Time
}

func NewAdminRepository(users *UserRepository) *AdminRepository {
	return &AdminRepository{users: users, now: time.Now}
}

// CreateStaffInput is a new account created by an administrator.
type CreateStaffInput struct {
	ActorID  string
	Email    string // normalized
	FullName string
	Password string
	Grants   []RoleGrant
	Reason   string
}

// ListFilter selects users for the administration list.
type ListFilter struct {
	StaffOnly bool // only users holding at least one role other than CUSTOMER
	Limit     int
	Offset    int
}

// legacyColumns derives the legacy single role / scope columns from the grants.
func legacyColumns(grants []RoleGrant) (role, scope string) {
	identity := User{Grants: grants}.Identity()
	role = "CUSTOMER"
	if len(identity.Roles) > 0 {
		role = identity.Roles[0]
	}
	scope = "NONE"
	switch {
	case len(identity.WarehouseScopes) == 0 && role != "CUSTOMER":
		scope = "ALL"
	case len(identity.WarehouseScopes) > 0:
		scope = identity.WarehouseScopes[0]
	}
	return role, scope
}

type queryer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func (r *AdminRepository) audit(ctx context.Context, q queryer, event, actorID, targetID, reason string, detail map[string]any) error {
	raw, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx, `
		INSERT INTO auth_audit_logs (event, actor_user_id, target_user_id, outcome, reason, detail, at)
		VALUES ($1, $2, $3, 'SUCCESS', $4, $5::jsonb, $6)
	`, event, actorID, targetID, reason, string(raw), r.now().UTC())
	return dependency.Wrap(err)
}

func grantNames(grants []RoleGrant) []string {
	out := make([]string, 0, len(grants))
	for _, g := range grants {
		if g.WarehouseID != "" {
			out = append(out, g.Role+"@"+g.WarehouseID)
		} else {
			out = append(out, g.Role)
		}
	}
	sort.Strings(out)
	return out
}

func insertGrants(ctx context.Context, q queryer, userID string, grants []RoleGrant) error {
	for _, g := range grants {
		var warehouse any
		if g.WarehouseID != "" {
			warehouse = g.WarehouseID
		}
		if _, err := q.Exec(ctx, `INSERT INTO user_roles (user_id, role, warehouse_id) VALUES ($1, $2, $3)`, userID, g.Role, warehouse); err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23503" {
				return ErrUnknownRole
			}
			return dependency.Wrap(err)
		}
	}
	return nil
}

// CreateStaff creates an active account with the given grants.
func (r *AdminRepository) CreateStaff(ctx context.Context, in CreateStaffInput) (User, error) {
	hash, err := password.Hash(in.Password)
	if err != nil {
		return User{}, err
	}
	id, err := newUserID()
	if err != nil {
		return User{}, err
	}
	role, scope := legacyColumns(in.Grants)
	u := User{ID: id, Email: NormalizeEmail(in.Email), Name: in.FullName, PasswordHash: hash, Role: role, WarehouseScope: scope, IsActive: true, Grants: in.Grants}

	tx, err := r.users.pool.Begin(ctx)
	if err != nil {
		return User{}, dependency.Wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		INSERT INTO users (id, email, full_name, password_hash, role, warehouse_scope, is_active)
		VALUES ($1, $2, $3, $4, $5, $6, TRUE)
	`, u.ID, u.Email, u.Name, u.PasswordHash, u.Role, u.WarehouseScope); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return User{}, ErrEmailTaken
		}
		return User{}, dependency.Wrap(err)
	}
	if err := insertGrants(ctx, tx, u.ID, in.Grants); err != nil {
		return User{}, err
	}
	if err := r.audit(ctx, tx, "ADMIN_USER_CREATED", in.ActorID, u.ID, in.Reason, map[string]any{"grants": grantNames(in.Grants)}); err != nil {
		return User{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, dependency.Wrap(err)
	}
	return u, nil
}

// SetRoles replaces all grants of targetID, signs the user out (refresh tokens are revoked so the next
// session gets the new roles) and records the change. Changing one's own access is refused.
func (r *AdminRepository) SetRoles(ctx context.Context, actorID, targetID string, grants []RoleGrant, reason string) (User, error) {
	if actorID == targetID {
		return User{}, ErrSelfModification
	}
	tx, err := r.users.pool.Begin(ctx)
	if err != nil {
		return User{}, dependency.Wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var exists bool
	if err := tx.QueryRow(ctx, `SELECT TRUE FROM users WHERE id = $1 FOR UPDATE`, targetID).Scan(&exists); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return User{}, ErrUserNotFound
		}
		return User{}, dependency.Wrap(err)
	}
	before, err := r.users.loadGrants(ctx, targetID)
	if err != nil {
		return User{}, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM user_roles WHERE user_id = $1`, targetID); err != nil {
		return User{}, dependency.Wrap(err)
	}
	if err := insertGrants(ctx, tx, targetID, grants); err != nil {
		return User{}, err
	}
	role, scope := legacyColumns(grants)
	if _, err := tx.Exec(ctx, `UPDATE users SET role = $2, warehouse_scope = $3 WHERE id = $1`, targetID, role, scope); err != nil {
		return User{}, dependency.Wrap(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE refresh_tokens SET revoked_at = $2 WHERE user_id = $1 AND revoked_at IS NULL`, targetID, r.now().UTC()); err != nil {
		return User{}, dependency.Wrap(err)
	}
	if err := r.audit(ctx, tx, "ADMIN_ROLES_CHANGED", actorID, targetID, reason,
		map[string]any{"before": grantNames(before), "after": grantNames(grants)}); err != nil {
		return User{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, dependency.Wrap(err)
	}
	return r.users.FindByID(ctx, targetID)
}

// Deactivate disables the account, revokes its sessions and records the change. Self-deactivation is refused.
func (r *AdminRepository) Deactivate(ctx context.Context, actorID, targetID, reason string) (User, error) {
	if actorID == targetID {
		return User{}, ErrSelfModification
	}
	tx, err := r.users.pool.Begin(ctx)
	if err != nil {
		return User{}, dependency.Wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `UPDATE users SET is_active = FALSE WHERE id = $1`, targetID)
	if err != nil {
		return User{}, dependency.Wrap(err)
	}
	if tag.RowsAffected() == 0 {
		return User{}, ErrUserNotFound
	}
	if _, err := tx.Exec(ctx, `UPDATE refresh_tokens SET revoked_at = $2 WHERE user_id = $1 AND revoked_at IS NULL`, targetID, r.now().UTC()); err != nil {
		return User{}, dependency.Wrap(err)
	}
	if err := r.audit(ctx, tx, "ADMIN_USER_DEACTIVATED", actorID, targetID, reason, map[string]any{}); err != nil {
		return User{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, dependency.Wrap(err)
	}
	return r.users.FindByID(ctx, targetID)
}

// ListUsers returns one page of users (with grants) and the total number of matching users.
func (r *AdminRepository) ListUsers(ctx context.Context, f ListFilter) ([]User, int, error) {
	where := ""
	if f.StaffOnly {
		where = ` WHERE EXISTS (SELECT 1 FROM user_roles ur WHERE ur.user_id = users.id AND ur.role <> 'CUSTOMER')`
	}
	var total int
	if err := r.users.pool.QueryRow(ctx, `SELECT count(*) FROM users`+where).Scan(&total); err != nil {
		return nil, 0, dependency.Wrap(err)
	}
	rows, err := r.users.pool.Query(ctx, selectUser+where+` ORDER BY created_at, id LIMIT $1 OFFSET $2`, f.Limit, f.Offset)
	if err != nil {
		return nil, 0, dependency.Wrap(err)
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Email, &u.Name, &u.PasswordHash, &u.Role, &u.WarehouseScope, &u.IsActive, &u.Phone); err != nil {
			return nil, 0, dependency.Wrap(err)
		}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, dependency.Wrap(err)
	}
	for i := range users {
		if users[i].Grants, err = r.users.loadGrants(ctx, users[i].ID); err != nil {
			return nil, 0, err
		}
	}
	return users, total, nil
}

// ValidGrants checks a requested grant list: known roles only, no duplicates, a warehouse only on warehouse roles.
// It returns a message per problem.
func ValidGrants(grants []RoleGrant) []string {
	if len(grants) == 0 {
		return []string{"At least one role is required"}
	}
	known := map[string]bool{}
	for _, role := range KnownRoles {
		known[role] = true
	}
	var problems []string
	seen := map[string]bool{}
	for _, g := range grants {
		switch {
		case !known[g.Role]:
			problems = append(problems, "Unknown role "+g.Role)
		case g.WarehouseID != "" && !IsWarehouseRole(g.Role):
			problems = append(problems, g.Role+" cannot be limited to a warehouse")
		case g.WarehouseID != "" && (len(g.WarehouseID) > 32 || strings.TrimSpace(g.WarehouseID) != g.WarehouseID):
			problems = append(problems, "Invalid warehouse id for "+g.Role)
		}
		key := g.Role + "@" + g.WarehouseID
		if seen[key] {
			problems = append(problems, "Duplicate grant "+key)
		}
		seen[key] = true
	}
	return problems
}
