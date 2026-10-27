package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrUserNotFound       = errors.New("không tìm thấy tài khoản")
	ErrInvalidCredentials = errors.New("email hoặc mật khẩu không chính xác")
)

type User struct {
	ID             string `json:"id"`
	Email          string `json:"email"`
	Name           string `json:"name"`
	PasswordHash   string `json:"-"`
	Role           string `json:"role"`
	WarehouseScope string `json:"warehouseScope"`
	IsActive       bool   `json:"isActive"`
}

type UserRepository struct {
	pool     *pgxpool.Pool
	mu       sync.RWMutex
	byEmail  map[string]User
	byUserID map[string]User
}

func HashPassword(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func NewUserRepository(pool *pgxpool.Pool) *UserRepository {
	defaultHash := HashPassword("123456")
	seeds := []User{
		{ID: "user-lan", Email: "lan@example.com", Name: "Khách hàng Lan", PasswordHash: defaultHash, Role: "CUSTOMER", WarehouseScope: "NONE", IsActive: true},
		{ID: "staff-wh-hcm", Email: "wh.hcm@commercelab.vn", Name: "Nhân viên Kho HCM", PasswordHash: defaultHash, Role: "WAREHOUSE_STAFF", WarehouseScope: "WH-HCM-01", IsActive: true},
		{ID: "staff-wh-hn", Email: "wh.hn@commercelab.vn", Name: "Nhân viên Kho HN", PasswordHash: defaultHash, Role: "WAREHOUSE_STAFF", WarehouseScope: "WH-HN-01", IsActive: true},
		{ID: "staff-cskh", Email: "cskh@commercelab.vn", Name: "Chuyên viên CSKH", PasswordHash: defaultHash, Role: "CSKH", WarehouseScope: "ALL", IsActive: true},
		{ID: "staff-finance", Email: "finance@commercelab.vn", Name: "Kế toán Đối soát", PasswordHash: defaultHash, Role: "FINANCE", WarehouseScope: "ALL", IsActive: true},
		{ID: "mgr-ops", Email: "ops.mgr@commercelab.vn", Name: "Quản lý Vận hành", PasswordHash: defaultHash, Role: "OPS_MGR", WarehouseScope: "ALL", IsActive: true},
	}
	byEmail := make(map[string]User, len(seeds))
	byID := make(map[string]User, len(seeds))
	for _, u := range seeds {
		byEmail[u.Email] = u
		byID[u.ID] = u
	}
	return &UserRepository{
		pool:     pool,
		byEmail:  byEmail,
		byUserID: byID,
	}
}

func (r *UserRepository) Authenticate(ctx context.Context, email, password string) (User, error) {
	u, err := r.FindByEmail(ctx, email)
	if err != nil {
		return User{}, ErrInvalidCredentials
	}
	if !u.IsActive || u.PasswordHash != HashPassword(password) {
		return User{}, ErrInvalidCredentials
	}
	return u, nil
}

func (r *UserRepository) FindByEmail(ctx context.Context, email string) (User, error) {
	if r.pool != nil {
		var u User
		err := r.pool.QueryRow(ctx, `
			SELECT id, email, full_name, password_hash, role, warehouse_scope, is_active
			FROM users
			WHERE email = $1
		`, email).Scan(&u.ID, &u.Email, &u.Name, &u.PasswordHash, &u.Role, &u.WarehouseScope, &u.IsActive)
		if err == nil {
			return u, nil
		}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	u, ok := r.byEmail[email]
	if !ok {
		return User{}, ErrUserNotFound
	}
	return u, nil
}

func (r *UserRepository) FindByID(ctx context.Context, id string) (User, error) {
	if r.pool != nil {
		var u User
		err := r.pool.QueryRow(ctx, `
			SELECT id, email, full_name, password_hash, role, warehouse_scope, is_active
			FROM users
			WHERE id = $1
		`, id).Scan(&u.ID, &u.Email, &u.Name, &u.PasswordHash, &u.Role, &u.WarehouseScope, &u.IsActive)
		if err == nil {
			return u, nil
		}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	u, ok := r.byUserID[id]
	if !ok {
		return User{}, ErrUserNotFound
	}
	return u, nil
}
