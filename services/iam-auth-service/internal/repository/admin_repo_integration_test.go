//go:build integration

package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

type adminFixture struct {
	repo *AdminRepository
	ctx  context.Context
}

func newAdminFixture(t *testing.T) *adminFixture {
	t.Helper()
	users, ctx := integrationRepo(t)
	return &adminFixture{repo: NewAdminRepository(users), ctx: ctx}
}

func (f *adminFixture) createStaff(t *testing.T, grants ...RoleGrant) User {
	t.Helper()
	u, err := f.repo.CreateStaff(f.ctx, CreateStaffInput{
		ActorID: "staff-admin", Email: fmt.Sprintf("staff-%d@commercelab.vn", time.Now().UnixNano()),
		FullName: "Staff Member", Password: "a-long-enough-password", Grants: grants, Reason: "new hire",
	})
	if err != nil {
		t.Fatalf("create staff: %v", err)
	}
	return u
}

type auditRow struct {
	event, actor, target, reason, detail string
}

func (f *adminFixture) auditOf(t *testing.T, targetID string) []auditRow {
	t.Helper()
	rows, err := f.repo.users.pool.Query(f.ctx, `
		SELECT event, COALESCE(actor_user_id, ''), COALESCE(target_user_id, ''), COALESCE(reason, ''), detail::text
		FROM auth_audit_logs WHERE target_user_id = $1 ORDER BY id`, targetID)
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	defer rows.Close()
	var out []auditRow
	for rows.Next() {
		var r auditRow
		if err := rows.Scan(&r.event, &r.actor, &r.target, &r.reason, &r.detail); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func TestCreateStaffStoresUserGrantsAndAudit(t *testing.T) {
	f := newAdminFixture(t)
	u := f.createStaff(t, RoleGrant{Role: "WAREHOUSE_STAFF", WarehouseID: "WH-HN-01"}, RoleGrant{Role: "SUPPORT_AGENT"})

	stored, err := f.repo.users.FindByID(f.ctx, u.ID)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	id := stored.Identity()
	if len(id.Roles) != 2 || id.Roles[0] != "SUPPORT_AGENT" || len(id.WarehouseScopes) != 1 || id.WarehouseScopes[0] != "WH-HN-01" {
		t.Fatalf("identity = %+v", id)
	}
	if _, err := f.repo.users.Authenticate(f.ctx, stored.Email, "a-long-enough-password"); err != nil {
		t.Fatalf("the new staff member must sign in: %v", err)
	}

	log := f.auditOf(t, u.ID)
	if len(log) != 1 || log[0].event != "ADMIN_USER_CREATED" || log[0].actor != "staff-admin" || log[0].reason != "new hire" {
		t.Fatalf("audit = %+v", log)
	}
	if strings.Contains(log[0].detail, stored.Email) || strings.Contains(log[0].detail, "commercelab.vn") {
		t.Fatalf("the audit detail must not contain an email address: %s", log[0].detail)
	}
	if !strings.Contains(log[0].detail, "WAREHOUSE_STAFF@WH-HN-01") {
		t.Fatalf("the audit detail must list the grants: %s", log[0].detail)
	}
}

func TestCreateStaffIsAtomic(t *testing.T) {
	f := newAdminFixture(t)
	email := fmt.Sprintf("atomic-%d@commercelab.vn", time.Now().UnixNano())

	_, err := f.repo.CreateStaff(f.ctx, CreateStaffInput{ActorID: "staff-admin", Email: email, FullName: "X", Password: "a-long-enough-password",
		Grants: []RoleGrant{{Role: "NOT_A_ROLE"}}, Reason: "test"})
	if !errors.Is(err, ErrUnknownRole) {
		t.Fatalf("err = %v, want ErrUnknownRole", err)
	}
	if _, err := f.repo.users.FindByEmail(f.ctx, email); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("a failed creation must leave no user behind, got %v", err)
	}

	dup := f.createStaff(t, RoleGrant{Role: "FINANCE"})
	_, err = f.repo.CreateStaff(f.ctx, CreateStaffInput{ActorID: "staff-admin", Email: dup.Email, FullName: "Y", Password: "a-long-enough-password",
		Grants: []RoleGrant{{Role: "FINANCE"}}, Reason: "dup"})
	if !errors.Is(err, ErrEmailTaken) {
		t.Fatalf("duplicate err = %v, want ErrEmailTaken", err)
	}
}

func TestSetRolesReplacesGrantsRevokesSessionsAndAudits(t *testing.T) {
	f := newAdminFixture(t)
	u := f.createStaff(t, RoleGrant{Role: "SUPPORT_AGENT"})
	tokens := NewRefreshRepository(f.repo.users)
	rec := RefreshTokenRecord{ID: "rt-admin-" + u.ID, UserID: u.ID, FamilyID: "fam-admin-" + u.ID, ExpiresAt: time.Now().Add(time.Hour)}
	if err := tokens.Create(f.ctx, rec, HashToken("raw-"+u.ID)); err != nil {
		t.Fatalf("create token: %v", err)
	}

	updated, err := f.repo.SetRoles(f.ctx, "staff-admin", u.ID, []RoleGrant{{Role: "FINANCE"}, {Role: "WAREHOUSE_MGR", WarehouseID: "WH-HCM-01"}}, "moved teams")
	if err != nil {
		t.Fatalf("set roles: %v", err)
	}
	id := updated.Identity()
	if len(id.Roles) != 2 || id.Roles[0] != "FINANCE" || id.Roles[1] != "WAREHOUSE_MGR" || id.WarehouseScopes[0] != "WH-HCM-01" {
		t.Fatalf("identity = %+v", id)
	}
	if _, _, err := (&refreshFixture{repo: tokens, ctx: f.ctx, now: time.Now()}).rotate(t, "raw-"+u.ID); !errors.Is(err, ErrRefreshReused) {
		t.Fatalf("existing sessions must be revoked after a role change, got %v", err)
	}

	log := f.auditOf(t, u.ID)
	last := log[len(log)-1]
	if last.event != "ADMIN_ROLES_CHANGED" || last.actor != "staff-admin" || last.reason != "moved teams" ||
		!strings.Contains(last.detail, "SUPPORT_AGENT") || !strings.Contains(last.detail, "FINANCE") {
		t.Fatalf("audit = %+v", last)
	}
}

func TestSetRolesRefusesSelfGrantAndUnknownTargets(t *testing.T) {
	f := newAdminFixture(t)
	before, _ := f.repo.users.FindByID(f.ctx, "staff-admin")

	if _, err := f.repo.SetRoles(f.ctx, "staff-admin", "staff-admin", []RoleGrant{{Role: "FINANCE_MGR"}}, "give myself more"); !errors.Is(err, ErrSelfModification) {
		t.Fatalf("self-grant err = %v, want ErrSelfModification", err)
	}
	after, _ := f.repo.users.FindByID(f.ctx, "staff-admin")
	if len(after.Grants) != len(before.Grants) || !after.HasRole("ACCESS_ADMIN") || after.HasRole("FINANCE_MGR") {
		t.Fatalf("a refused self-grant must change nothing: before %+v after %+v", before.Grants, after.Grants)
	}
	if _, err := f.repo.SetRoles(f.ctx, "staff-admin", "no-such-user", []RoleGrant{{Role: "FINANCE"}}, "x"); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("unknown target err = %v, want ErrUserNotFound", err)
	}
	u := f.createStaff(t, RoleGrant{Role: "FINANCE"})
	if _, err := f.repo.SetRoles(f.ctx, "staff-admin", u.ID, []RoleGrant{{Role: "NOT_A_ROLE"}}, "x"); !errors.Is(err, ErrUnknownRole) {
		t.Fatalf("unknown role err = %v, want ErrUnknownRole", err)
	}
	if kept, _ := f.repo.users.FindByID(f.ctx, u.ID); !kept.HasRole("FINANCE") {
		t.Fatalf("a failed role change must be rolled back, grants = %+v", kept.Grants)
	}
}

func TestDeactivateBlocksSignInRevokesSessionsAndAudits(t *testing.T) {
	f := newAdminFixture(t)
	u := f.createStaff(t, RoleGrant{Role: "SUPPORT_AGENT"})
	tokens := NewRefreshRepository(f.repo.users)
	rec := RefreshTokenRecord{ID: "rt-deact-" + u.ID, UserID: u.ID, FamilyID: "fam-deact-" + u.ID, ExpiresAt: time.Now().Add(time.Hour)}
	if err := tokens.Create(f.ctx, rec, HashToken("raw-"+u.ID)); err != nil {
		t.Fatal(err)
	}

	if _, err := f.repo.Deactivate(f.ctx, "staff-admin", "staff-admin", "oops"); !errors.Is(err, ErrSelfModification) {
		t.Fatalf("self-deactivation err = %v, want ErrSelfModification", err)
	}
	got, err := f.repo.Deactivate(f.ctx, "staff-admin", u.ID, "left the company")
	if err != nil || got.IsActive {
		t.Fatalf("deactivate: user=%+v err=%v", got, err)
	}
	if _, err := f.repo.users.Authenticate(f.ctx, u.Email, "a-long-enough-password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("a deactivated user must not sign in, got %v", err)
	}
	if _, _, err := (&refreshFixture{repo: tokens, ctx: f.ctx, now: time.Now()}).rotate(t, "raw-"+u.ID); !errors.Is(err, ErrRefreshReused) {
		t.Fatalf("sessions must be revoked on deactivation, got %v", err)
	}
	log := f.auditOf(t, u.ID)
	if last := log[len(log)-1]; last.event != "ADMIN_USER_DEACTIVATED" || last.actor != "staff-admin" || last.reason != "left the company" {
		t.Fatalf("audit = %+v", last)
	}
	if _, err := f.repo.Deactivate(f.ctx, "staff-admin", "no-such-user", "x"); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("unknown target err = %v, want ErrUserNotFound", err)
	}
}

func TestListUsersFiltersStaffAndPaginates(t *testing.T) {
	f := newAdminFixture(t)
	staff := f.createStaff(t, RoleGrant{Role: "PRODUCT_STAFF"})
	customer, err := f.repo.users.Register(f.ctx, fmt.Sprintf("list-%d@example.com", time.Now().UnixNano()), "a-long-enough-password", "Customer")
	if err != nil {
		t.Fatal(err)
	}

	all, totalAll, err := f.repo.ListUsers(f.ctx, ListFilter{Limit: 1000})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	staffOnly, totalStaff, err := f.repo.ListUsers(f.ctx, ListFilter{StaffOnly: true, Limit: 1000})
	if err != nil {
		t.Fatalf("list staff: %v", err)
	}
	has := func(users []User, id string) bool {
		for _, u := range users {
			if u.ID == id {
				return true
			}
		}
		return false
	}
	if !has(all, customer.ID) || !has(all, staff.ID) || totalAll != len(all) {
		t.Fatalf("the full list must include customers and staff (total %d, got %d)", totalAll, len(all))
	}
	if has(staffOnly, customer.ID) || !has(staffOnly, staff.ID) || totalStaff != len(staffOnly) || totalStaff >= totalAll {
		t.Fatalf("the staff list must exclude customers (staff %d, all %d)", totalStaff, totalAll)
	}
	page, total, err := f.repo.ListUsers(f.ctx, ListFilter{Limit: 2, Offset: 1})
	if err != nil || len(page) != 2 || total != totalAll {
		t.Fatalf("page = %d users, total %d (want 2 and %d), err %v", len(page), total, totalAll, err)
	}
	for _, u := range page {
		if len(u.Grants) == 0 {
			t.Fatalf("listed users must carry their grants: %+v", u)
		}
	}
}

func TestValidGrants(t *testing.T) {
	tests := []struct {
		name   string
		grants []RoleGrant
		ok     bool
	}{
		{"single role", []RoleGrant{{Role: "FINANCE"}}, true},
		{"warehouse role with a warehouse", []RoleGrant{{Role: "WAREHOUSE_STAFF", WarehouseID: "WH-HCM-01"}}, true},
		{"warehouse role without a warehouse (all)", []RoleGrant{{Role: "WAREHOUSE_MGR"}}, true},
		{"empty", nil, false},
		{"unknown role", []RoleGrant{{Role: "SUPERUSER"}}, false},
		{"warehouse on a non-warehouse role", []RoleGrant{{Role: "FINANCE", WarehouseID: "WH-HCM-01"}}, false},
		{"duplicate", []RoleGrant{{Role: "FINANCE"}, {Role: "FINANCE"}}, false},
		{"overlong warehouse id", []RoleGrant{{Role: "WAREHOUSE_STAFF", WarehouseID: strings.Repeat("W", 33)}}, false},
	}
	for _, tc := range tests {
		if got := len(ValidGrants(tc.grants)) == 0; got != tc.ok {
			t.Errorf("%s: valid = %v, want %v (%v)", tc.name, got, tc.ok, ValidGrants(tc.grants))
		}
	}
}
