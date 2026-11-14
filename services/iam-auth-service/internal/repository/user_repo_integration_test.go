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

func TestRegisterStoresCustomerAndRejectsDuplicateEmail(t *testing.T) {
	repo, ctx := integrationRepo(t)
	email := fmt.Sprintf("it-%d@example.com", time.Now().UnixNano())

	u, err := repo.Register(ctx, email, "a-long-enough-password", "Integration User")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if u.Role != "CUSTOMER" || u.WarehouseScope != "NONE" || !u.IsActive || !strings.HasPrefix(u.ID, "usr-") {
		t.Fatalf("unexpected user %+v", u)
	}

	signedIn, err := repo.Authenticate(ctx, strings.ToUpper(email), "a-long-enough-password")
	if err != nil || signedIn.ID != u.ID {
		t.Fatalf("the new account must sign in: user=%+v err=%v", signedIn, err)
	}

	// The database unique constraint, not just a pre-check, rejects the duplicate (also under concurrency).
	if _, err := repo.Register(ctx, email, "another-long-password", "Someone Else"); !errors.Is(err, ErrEmailTaken) {
		t.Fatalf("duplicate err = %v, want ErrEmailTaken", err)
	}
}

func TestConcurrentRegistrationsOfOneEmailCreateExactlyOneAccount(t *testing.T) {
	repo, ctx := integrationRepo(t)
	email := fmt.Sprintf("race-%d@example.com", time.Now().UnixNano())

	const attempts = 6
	results := make(chan error, attempts)
	for i := 0; i < attempts; i++ {
		go func() {
			_, err := repo.Register(ctx, email, "a-long-enough-password", "Racer")
			results <- err
		}()
	}
	created, taken := 0, 0
	for i := 0; i < attempts; i++ {
		switch err := <-results; {
		case err == nil:
			created++
		case errors.Is(err, ErrEmailTaken):
			taken++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if created != 1 || taken != attempts-1 {
		t.Fatalf("created %d, taken %d; want exactly 1 and %d", created, taken, attempts-1)
	}
}

func grantsOf(t *testing.T, repo *UserRepository, ctx context.Context, userID string) []RoleGrant {
	t.Helper()
	u, err := repo.FindByID(ctx, userID)
	if err != nil {
		t.Fatalf("find %s: %v", userID, err)
	}
	return u.Grants
}

func TestSeedUsersGetBlueprintRolesFromTheLegacyColumns(t *testing.T) {
	repo, ctx := integrationRepo(t)

	cases := []struct {
		userID     string
		wantRole   string
		wantScopes []string
	}{
		{"user-lan", "CUSTOMER", nil},
		{"staff-wh-hcm", "WAREHOUSE_STAFF", []string{"WH-HCM-01"}},
		{"staff-wh-hn", "WAREHOUSE_STAFF", []string{"WH-HN-01"}},
		{"staff-cskh", "SUPPORT_AGENT", nil},
		{"staff-finance", "FINANCE", nil},
		{"mgr-ops", "OPS_MGR", nil},
		{"staff-admin", "ACCESS_ADMIN", nil},
	}
	for _, tc := range cases {
		u, err := repo.FindByID(ctx, tc.userID)
		if err != nil {
			t.Fatalf("find %s: %v", tc.userID, err)
		}
		id := u.Identity()
		if len(id.Roles) != 1 || id.Roles[0] != tc.wantRole {
			t.Errorf("%s roles = %v, want [%s]", tc.userID, id.Roles, tc.wantRole)
		}
		if strings.Join(id.WarehouseScopes, ",") != strings.Join(tc.wantScopes, ",") {
			t.Errorf("%s scopes = %v, want %v", tc.userID, id.WarehouseScopes, tc.wantScopes)
		}
	}
}

func TestDevAccessAdminCanSignIn(t *testing.T) {
	repo, ctx := integrationRepo(t)
	u, err := repo.Authenticate(ctx, "admin@commercelab.vn", devPassword)
	if err != nil {
		t.Fatalf("the dev ACCESS_ADMIN must sign in with the dev password: %v", err)
	}
	if !u.HasRole("ACCESS_ADMIN") {
		t.Fatalf("grants = %+v", u.Grants)
	}
}

func TestRegisteredCustomerHoldsTheCustomerGrant(t *testing.T) {
	repo, ctx := integrationRepo(t)
	u, err := repo.Register(ctx, fmt.Sprintf("grant-%d@example.com", time.Now().UnixNano()), "a-long-enough-password", "Grant Test")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	grants := grantsOf(t, repo, ctx, u.ID)
	if len(grants) != 1 || grants[0].Role != "CUSTOMER" || grants[0].WarehouseID != "" {
		t.Fatalf("grants = %+v, want exactly the CUSTOMER grant", grants)
	}
}

func TestRoleGrantsAreConstrainedByTheDatabase(t *testing.T) {
	repo, ctx := integrationRepo(t)

	if _, err := repo.pool.Exec(ctx, `INSERT INTO user_roles (user_id, role) VALUES ('user-lan', 'NOT_A_ROLE')`); err == nil {
		t.Fatal("an unknown role must be rejected by the foreign key")
	}
	if _, err := repo.pool.Exec(ctx, `INSERT INTO user_roles (user_id, role) VALUES ('user-lan', 'CUSTOMER')`); err == nil {
		t.Fatal("granting the same role twice must be rejected")
	}
	if _, err := repo.pool.Exec(ctx, `INSERT INTO user_roles (user_id, role) VALUES ('no-such-user', 'CUSTOMER')`); err == nil {
		t.Fatal("a grant for an unknown user must be rejected")
	}
	var permissions int
	if err := repo.pool.QueryRow(ctx, `SELECT count(*) FROM role_permissions WHERE role = 'ACCESS_ADMIN'`).Scan(&permissions); err != nil || permissions == 0 {
		t.Fatalf("ACCESS_ADMIN must hold permissions, got %d (err %v)", permissions, err)
	}
	var roles int
	if err := repo.pool.QueryRow(ctx, `SELECT count(*) FROM roles`).Scan(&roles); err != nil || roles != 13 {
		t.Fatalf("roles = %d (err %v), want the 13 blueprint roles", roles, err)
	}
}

func registerForTest(t *testing.T, repo *UserRepository, ctx context.Context) User {
	t.Helper()
	u, err := repo.Register(ctx, fmt.Sprintf("p-%d@example.com", time.Now().UnixNano()), "a-long-enough-password", "Profile Test")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	return u
}

func TestUpdateProfilePersistsNameAndPhone(t *testing.T) {
	repo, ctx := integrationRepo(t)
	u := registerForTest(t, repo, ctx)

	name, phone := "New Name", "+84901234567"
	updated, err := repo.UpdateProfile(ctx, u.ID, ProfileUpdate{FullName: &name, Phone: &phone})
	if err != nil || updated.Name != "New Name" || updated.Phone != "+84901234567" {
		t.Fatalf("update: user=%+v err=%v", updated, err)
	}

	onlyName := "Only Name"
	updated, err = repo.UpdateProfile(ctx, u.ID, ProfileUpdate{FullName: &onlyName})
	if err != nil || updated.Name != "Only Name" || updated.Phone != "+84901234567" {
		t.Fatalf("a missing phone must stay unchanged: user=%+v err=%v", updated, err)
	}

	empty := ""
	updated, err = repo.UpdateProfile(ctx, u.ID, ProfileUpdate{Phone: &empty})
	if err != nil || updated.Phone != "" || updated.Name != "Only Name" {
		t.Fatalf("an empty phone must clear it: user=%+v err=%v", updated, err)
	}
	if _, err := repo.UpdateProfile(ctx, "no-such-user", ProfileUpdate{FullName: &name}); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("unknown user err = %v, want ErrUserNotFound", err)
	}
}

func TestChangePasswordChecksTheCurrentPasswordAgainstTheStoredHash(t *testing.T) {
	repo, ctx := integrationRepo(t)
	u := registerForTest(t, repo, ctx)

	if err := repo.ChangePassword(ctx, u.ID, "not-the-password", "another-long-password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong current password err = %v, want ErrInvalidCredentials", err)
	}
	if _, err := repo.Authenticate(ctx, u.Email, "a-long-enough-password"); err != nil {
		t.Fatalf("a rejected change must keep the old password: %v", err)
	}
	if err := repo.ChangePassword(ctx, u.ID, "a-long-enough-password", "another-long-password"); err != nil {
		t.Fatalf("change: %v", err)
	}
	if _, err := repo.Authenticate(ctx, u.Email, "another-long-password"); err != nil {
		t.Fatalf("the new password must sign in: %v", err)
	}
	if _, err := repo.Authenticate(ctx, u.Email, "a-long-enough-password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("the old password must stop working, got %v", err)
	}
}
