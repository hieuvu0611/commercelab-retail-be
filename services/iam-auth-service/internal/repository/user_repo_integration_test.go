//go:build integration

package repository

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/db"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/password"
	"golang.org/x/crypto/argon2"
)

// devPassword is the development seed password documented in .env.example (DEV_SEED_PASSWORD).
const devPassword = "Dev-Passw0rd-2026"

func integrationRepo(t *testing.T) (*UserRepository, context.Context) {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL_IAM")
	if dsn == "" {
		t.Skip("DATABASE_URL_IAM is not set; run `make test-integration`")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	pool, err := db.ConnectPostgres(ctx, dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	t.Cleanup(pool.Close)
	return NewUserRepository(pool), ctx
}

func TestSeedUsersSignInWithArgon2idDevPassword(t *testing.T) {
	repo, ctx := integrationRepo(t)

	u, err := repo.Authenticate(ctx, "  LAN@example.com ", devPassword)
	if err != nil {
		t.Fatalf("seed customer must sign in with the dev password: %v", err)
	}
	if u.ID != "user-lan" || !strings.HasPrefix(u.PasswordHash, "$argon2id$") {
		t.Fatalf("unexpected user %+v", u)
	}
	if _, err := repo.Authenticate(ctx, "lan@example.com", "123456"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("the old SHA-256 seed password must no longer work, got %v", err)
	}
	if _, err := repo.Authenticate(ctx, "nobody@example.com", devPassword); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("unknown email must look like wrong credentials, got %v", err)
	}
}

// weakHashOf builds a valid Argon2id hash with deliberately weak parameters.
func weakHashOf(plain string) string {
	salt := []byte("0123456789abcdef")
	key := argon2.IDKey([]byte(plain), salt, 1, 4096, 1, 32)
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=19$m=4096,t=1,p=1$%s$%s", enc.EncodeToString(salt), enc.EncodeToString(key))
}

func TestLoginUpgradesOutdatedHashParameters(t *testing.T) {
	repo, ctx := integrationRepo(t)

	weak := weakHashOf(devPassword)
	if !password.NeedsRehash(weak) {
		t.Fatal("test setup: the weak hash must need a rehash")
	}
	if _, err := repo.pool.Exec(ctx, `UPDATE users SET password_hash = $1 WHERE id = 'staff-finance'`, weak); err != nil {
		t.Fatalf("store weak hash: %v", err)
	}
	if _, err := repo.Authenticate(ctx, "finance@commercelab.vn", devPassword); err != nil {
		t.Fatalf("login with a weak but valid hash must succeed: %v", err)
	}
	u, err := repo.FindByID(ctx, "staff-finance")
	if err != nil {
		t.Fatalf("find user: %v", err)
	}
	if password.NeedsRehash(u.PasswordHash) {
		t.Fatalf("hash was not upgraded after login: %s", u.PasswordHash)
	}
}

func TestStoreErrorsAreMarkedUnavailable(t *testing.T) {
	repo, ctx := integrationRepo(t)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := repo.FindByID(cancelled, "user-lan"); !dependency.IsUnavailable(err) {
		t.Fatalf("a failing store must be reported as unavailable, got %v", err)
	}
}
