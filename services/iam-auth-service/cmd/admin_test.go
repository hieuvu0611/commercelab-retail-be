package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth/authtest"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/services/iam-auth-service/internal/repository"
)

// fakeAdmin records the calls the handlers make; the real transactional behaviour is covered by the
// repository integration tests.
type fakeAdmin struct {
	created   []repository.CreateStaffInput
	setRoles  []string
	deactived []string
	err       error
	users     []repository.User
}

func (f *fakeAdmin) CreateStaff(_ context.Context, in repository.CreateStaffInput) (repository.User, error) {
	if f.err != nil {
		return repository.User{}, f.err
	}
	f.created = append(f.created, in)
	return repository.User{ID: "usr-new", Email: in.Email, Name: in.FullName, IsActive: true, Grants: in.Grants}, nil
}

func (f *fakeAdmin) SetRoles(_ context.Context, actorID, targetID string, grants []repository.RoleGrant, reason string) (repository.User, error) {
	if f.err != nil {
		return repository.User{}, f.err
	}
	f.setRoles = append(f.setRoles, actorID+"->"+targetID+":"+reason)
	return repository.User{ID: targetID, IsActive: true, Grants: grants}, nil
}

func (f *fakeAdmin) Deactivate(_ context.Context, actorID, targetID, reason string) (repository.User, error) {
	if f.err != nil {
		return repository.User{}, f.err
	}
	f.deactived = append(f.deactived, actorID+"->"+targetID+":"+reason)
	return repository.User{ID: targetID, IsActive: false}, nil
}

func (f *fakeAdmin) ListUsers(_ context.Context, _ repository.ListFilter) ([]repository.User, int, error) {
	if f.err != nil {
		return nil, 0, f.err
	}
	return f.users, len(f.users), nil
}

func adminServer(t *testing.T, admin AdminStore) http.Handler {
	t.Helper()
	return NewServerWithAdmin(newFakeStore(t), newFakeTokens(), admin, true)
}

func bearerOf(t *testing.T, id auth.Identity) string {
	t.Helper()
	tok, err := auth.SignAccessToken(id)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

var (
	adminIdentity    = auth.Identity{UserID: "staff-admin", Roles: []string{"ACCESS_ADMIN"}}
	customerIdentity = auth.Identity{UserID: "user-lan", Roles: []string{"CUSTOMER"}}
	financeIdentity  = auth.Identity{UserID: "staff-finance", Roles: []string{"FINANCE"}}
)

const validCreate = `{"email":"new.staff@commercelab.vn","fullName":"New Staff","password":"a-long-enough-password","roles":[{"role":"WAREHOUSE_STAFF","warehouseId":"WH-HN-01"}],"reason":"new hire"}`

func TestAdminAPIIsOnlyForAccessAdmins(t *testing.T) {
	authtest.Configure(t)
	fake := &fakeAdmin{}
	srv := adminServer(t, fake)

	calls := []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/admin/users", ""},
		{http.MethodPost, "/api/v1/admin/users", validCreate},
		{http.MethodPut, "/api/v1/admin/users/usr-1/roles", `{"roles":[{"role":"FINANCE"}],"reason":"moved"}`},
		{http.MethodPost, "/api/v1/admin/users/usr-1/deactivate", `{"reason":"left"}`},
	}
	for _, c := range calls {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			if rec := authedRequest(t, srv, c.method, c.path, "", c.body); rec.Code != http.StatusUnauthorized {
				t.Fatalf("anonymous: %d; want 401", rec.Code)
			}
			for name, id := range map[string]auth.Identity{"customer": customerIdentity, "finance staff": financeIdentity} {
				if rec := authedRequest(t, srv, c.method, c.path, bearerOf(t, id), c.body); rec.Code != http.StatusForbidden {
					t.Fatalf("%s: %d %s; want 403", name, rec.Code, rec.Body.String())
				}
			}
		})
	}
	if len(fake.created)+len(fake.setRoles)+len(fake.deactived) != 0 {
		t.Fatal("a rejected caller must not reach the store")
	}
	// The legacy role claim alone does not make an administrator.
	legacy := auth.Identity{UserID: "x", Role: "ACCESS_ADMIN"}
	if rec := authedRequest(t, srv, http.MethodGet, "/api/v1/admin/users", bearerOf(t, legacy), ""); rec.Code != http.StatusForbidden {
		t.Fatalf("legacy role claim: %d; want 403", rec.Code)
	}
}

func TestAdminCreatesAStaffUser(t *testing.T) {
	authtest.Configure(t)
	fake := &fakeAdmin{}
	srv := adminServer(t, fake)

	rec := authedRequest(t, srv, http.MethodPost, "/api/v1/admin/users", bearerOf(t, adminIdentity), validCreate)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	if len(fake.created) != 1 {
		t.Fatalf("created = %d", len(fake.created))
	}
	in := fake.created[0]
	if in.ActorID != "staff-admin" || in.Email != "new.staff@commercelab.vn" || in.Reason != "new hire" ||
		len(in.Grants) != 1 || in.Grants[0].Role != "WAREHOUSE_STAFF" || in.Grants[0].WarehouseID != "WH-HN-01" {
		t.Fatalf("store input = %+v", in)
	}
	if strings.Contains(rec.Body.String(), "password") || strings.Contains(rec.Body.String(), "argon2id") {
		t.Fatalf("the response must not echo password material: %s", rec.Body.String())
	}
	var resp struct {
		Data adminUserJSON `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || len(resp.Data.Roles) != 1 || resp.Data.Roles[0].WarehouseID != "WH-HN-01" {
		t.Fatalf("response = %+v err=%v", resp.Data, err)
	}
}

func TestAdminCreateValidatesInput(t *testing.T) {
	authtest.Configure(t)
	fake := &fakeAdmin{}
	srv := adminServer(t, fake)
	base := func(mutate func(m map[string]any)) string {
		m := map[string]any{"email": "a@commercelab.vn", "fullName": "A", "password": "a-long-enough-password",
			"roles": []map[string]string{{"role": "FINANCE"}}, "reason": "new hire"}
		mutate(m)
		raw, _ := json.Marshal(m)
		return string(raw)
	}
	tests := []struct {
		name  string
		body  string
		field string
	}{
		{"bad email", base(func(m map[string]any) { m["email"] = "nope" }), "email"},
		{"weak password", base(func(m map[string]any) { m["password"] = "short" }), "password"},
		{"no roles", base(func(m map[string]any) { m["roles"] = []map[string]string{} }), "roles"},
		{"unknown role", base(func(m map[string]any) { m["roles"] = []map[string]string{{"role": "SUPERUSER"}} }), "roles"},
		{"warehouse on a non-warehouse role", base(func(m map[string]any) { m["roles"] = []map[string]string{{"role": "FINANCE", "warehouseId": "WH-1"}} }), "roles"},
		{"missing reason", base(func(m map[string]any) { delete(m, "reason") }), "reason"},
		{"short reason", base(func(m map[string]any) { m["reason"] = "x" }), "reason"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := authedRequest(t, srv, http.MethodPost, "/api/v1/admin/users", bearerOf(t, adminIdentity), tc.body)
			if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"`+tc.field+`"`) {
				t.Fatalf("%d %s; want 422 naming %s", rec.Code, rec.Body.String(), tc.field)
			}
		})
	}
	if rec := authedRequest(t, srv, http.MethodPost, "/api/v1/admin/users", bearerOf(t, adminIdentity),
		`{"email":"a@commercelab.vn","fullName":"A","password":"a-long-enough-password","roles":[{"role":"FINANCE"}],"reason":"hire","isActive":false}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown field: %d; want 400", rec.Code)
	}
	if len(fake.created) != 0 {
		t.Fatal("invalid requests must not reach the store")
	}
}

func TestAdminCannotChangeOwnAccess(t *testing.T) {
	authtest.Configure(t)
	fake := &fakeAdmin{}
	srv := adminServer(t, fake)
	token := bearerOf(t, adminIdentity)

	rec := authedRequest(t, srv, http.MethodPut, "/api/v1/admin/users/staff-admin/roles", token, `{"roles":[{"role":"FINANCE_MGR"}],"reason":"grant myself"}`)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "CANNOT_MODIFY_OWN_ACCESS") {
		t.Fatalf("self-grant: %d %s; want 403 CANNOT_MODIFY_OWN_ACCESS", rec.Code, rec.Body.String())
	}
	rec = authedRequest(t, srv, http.MethodPost, "/api/v1/admin/users/staff-admin/deactivate", token, `{"reason":"lock myself out"}`)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "CANNOT_MODIFY_OWN_ACCESS") {
		t.Fatalf("self-deactivation: %d %s; want 403 CANNOT_MODIFY_OWN_ACCESS", rec.Code, rec.Body.String())
	}
	if len(fake.setRoles)+len(fake.deactived) != 0 {
		t.Fatal("self-modification must be refused before the store is called")
	}
}

func TestAdminChangesRolesAndDeactivatesAnotherUser(t *testing.T) {
	authtest.Configure(t)
	fake := &fakeAdmin{}
	srv := adminServer(t, fake)
	token := bearerOf(t, adminIdentity)

	rec := authedRequest(t, srv, http.MethodPut, "/api/v1/admin/users/usr-7/roles", token,
		`{"roles":[{"role":"WAREHOUSE_MGR","warehouseId":"WH-HCM-01"},{"role":"SUPPORT_AGENT"}],"reason":"moved teams"}`)
	if rec.Code != http.StatusOK || len(fake.setRoles) != 1 || fake.setRoles[0] != "staff-admin->usr-7:moved teams" {
		t.Fatalf("set roles: %d %s calls=%v", rec.Code, rec.Body.String(), fake.setRoles)
	}
	rec = authedRequest(t, srv, http.MethodPost, "/api/v1/admin/users/usr-7/deactivate", token, `{"reason":"left the company"}`)
	if rec.Code != http.StatusOK || len(fake.deactived) != 1 || fake.deactived[0] != "staff-admin->usr-7:left the company" {
		t.Fatalf("deactivate: %d %s calls=%v", rec.Code, rec.Body.String(), fake.deactived)
	}
	if !strings.Contains(rec.Body.String(), `"isActive":false`) {
		t.Fatalf("response must show the user inactive: %s", rec.Body.String())
	}
	if rec := authedRequest(t, srv, http.MethodPost, "/api/v1/admin/users/usr-7/deactivate", token, `{}`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a reason is required: %d", rec.Code)
	}
}

func TestAdminErrorsAreMappedToStatusCodes(t *testing.T) {
	authtest.Configure(t)
	token := bearerOf(t, adminIdentity)
	tests := []struct {
		name string
		err  error
		want int
		code string
	}{
		{"unknown user", repository.ErrUserNotFound, http.StatusNotFound, "USER_NOT_FOUND"},
		{"duplicate email", repository.ErrEmailTaken, http.StatusConflict, "EMAIL_ALREADY_REGISTERED"},
		{"unknown role", repository.ErrUnknownRole, http.StatusUnprocessableEntity, "VALIDATION_FAILED"},
		{"store down", repositoryUnavailable(), http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := adminServer(t, &fakeAdmin{err: tc.err})
			rec := authedRequest(t, srv, http.MethodPost, "/api/v1/admin/users", token, validCreate)
			if rec.Code != tc.want || !strings.Contains(rec.Body.String(), tc.code) {
				t.Fatalf("%d %s; want %d %s", rec.Code, rec.Body.String(), tc.want, tc.code)
			}
		})
	}
}

func TestAdminListsUsersWithPaging(t *testing.T) {
	authtest.Configure(t)
	fake := &fakeAdmin{users: []repository.User{
		{ID: "usr-1", Email: "a@commercelab.vn", Name: "A", IsActive: true, Grants: []repository.RoleGrant{{Role: "FINANCE"}}},
		{ID: "usr-2", Email: "b@commercelab.vn", Name: "B", IsActive: false, Grants: []repository.RoleGrant{{Role: "WAREHOUSE_STAFF", WarehouseID: "WH-HCM-01"}}},
	}}
	srv := adminServer(t, fake)
	token := bearerOf(t, adminIdentity)

	rec := authedRequest(t, srv, http.MethodGet, "/api/v1/admin/users?staff=true&limit=10", token, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data     []adminUserJSON `json:"data"`
		Metadata struct {
			Total int64 `json:"total"`
			Limit int   `json:"limit"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if len(resp.Data) != 2 || resp.Data[1].Roles[0].WarehouseID != "WH-HCM-01" || resp.Metadata.Total != 2 || resp.Metadata.Limit != 10 {
		t.Fatalf("response = %+v", resp)
	}
	for _, q := range []string{"limit=0", "limit=101", "limit=abc", "offset=-1"} {
		if rec := authedRequest(t, srv, http.MethodGet, "/api/v1/admin/users?"+q, token, ""); rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("?%s: %d; want 422", q, rec.Code)
		}
	}
}

func TestAdminAPIIsAbsentWithoutAnAdminStore(t *testing.T) {
	authtest.Configure(t)
	srv := NewServer(newFakeStore(t), newFakeTokens(), true)
	if rec := authedRequest(t, srv, http.MethodGet, "/api/v1/admin/users", bearerOf(t, adminIdentity), ""); rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d; the admin API must not exist without a store", rec.Code)
	}
}

func repositoryUnavailable() error { return dependency.Wrap(errors.New("connection refused")) }
