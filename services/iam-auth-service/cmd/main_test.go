package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/password"
	"github.com/hieuvu0611/commercelab-retail-be/services/iam-auth-service/internal/repository"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAuthLoginMeAndRefreshFlow(t *testing.T) {
	srv := NewServer(newFakeStore(t), true)

	// 1. Đăng nhập với tài khoản Nhân viên Kho HCM (I08: WarehouseScope = WH-HCM-01)
	loginBody := []byte(`{"email":"wh.hcm@commercelab.vn","password":"Dev-Passw0rd-2026"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(loginBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on login, got %d: %s", rec.Code, rec.Body.String())
	}

	var loginResp struct {
		Data UserProfile `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &loginResp); err != nil {
		t.Fatalf("failed to decode login response: %v", err)
	}
	if loginResp.Data.WarehouseScope != "WH-HCM-01" || loginResp.Data.AccessToken == "" || loginResp.Data.RefreshToken == "" {
		t.Fatalf("unexpected login data: %+v", loginResp.Data)
	}

	// 2. Gọi GET /api/v1/auth/me bằng Access Token
	meReq := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	meReq.Header.Set("Authorization", "Bearer "+loginResp.Data.AccessToken)
	meRec := httptest.NewRecorder()
	srv.ServeHTTP(meRec, meReq)

	if meRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on /me, got %d: %s", meRec.Code, meRec.Body.String())
	}

	// 3. Làm mới token qua POST /auth/refresh
	refBody, _ := json.Marshal(map[string]string{"refreshToken": loginResp.Data.RefreshToken})
	refReq := httptest.NewRequest(http.MethodPost, "/auth/refresh", bytes.NewReader(refBody))
	refRec := httptest.NewRecorder()
	srv.ServeHTTP(refRec, refReq)

	if refRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on /auth/refresh, got %d: %s", refRec.Code, refRec.Body.String())
	}
}

func TestLoginValidationResponse(t *testing.T) {
	srv := NewServer(newFakeStore(t), true)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBufferString(`{"email":""}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	var body struct {
		Code          string              `json:"code"`
		Errors        map[string][]string `json:"errors"`
		CorrelationID string              `json:"correlationId"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusUnprocessableEntity || body.Code != "VALIDATION_FAILED" || len(body.Errors["email"]) == 0 || len(body.Errors["password"]) == 0 || body.CorrelationID == "" {
		t.Fatalf("unexpected validation response: %d %+v", rec.Code, body)
	}
}

// fakeStore is an in-memory UserStore for handler tests; production code has no in-memory users.
type fakeStore struct {
	byID map[string]repository.User
}

const testPassword = "Dev-Passw0rd-2026"

func newFakeStore(t *testing.T) *fakeStore {
	t.Helper()
	hash, err := password.Hash(testPassword)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	users := []repository.User{
		{ID: "user-lan", Email: "lan@example.com", Name: "Lan", PasswordHash: hash, Role: "CUSTOMER", WarehouseScope: "NONE", IsActive: true},
		{ID: "staff-wh-hcm", Email: "wh.hcm@commercelab.vn", Name: "Warehouse HCM", PasswordHash: hash, Role: "WAREHOUSE_STAFF", WarehouseScope: "WH-HCM-01", IsActive: true},
		{ID: "user-off", Email: "off@example.com", Name: "Off", PasswordHash: hash, Role: "CUSTOMER", WarehouseScope: "NONE", IsActive: false},
	}
	store := &fakeStore{byID: map[string]repository.User{}}
	for _, u := range users {
		store.byID[u.ID] = u
	}
	return store
}

func (f *fakeStore) Authenticate(_ context.Context, email, plain string) (repository.User, error) {
	for _, u := range f.byID {
		if u.Email != email {
			continue
		}
		if ok, _ := password.Verify(plain, u.PasswordHash); ok && u.IsActive {
			return u, nil
		}
	}
	return repository.User{}, repository.ErrInvalidCredentials
}

func (f *fakeStore) FindByID(_ context.Context, id string) (repository.User, error) {
	if u, ok := f.byID[id]; ok {
		return u, nil
	}
	return repository.User{}, repository.ErrUserNotFound
}

func TestLoginRejectsWrongPasswordUnknownEmailAndInactiveUser(t *testing.T) {
	srv := NewServer(newFakeStore(t), true)
	for name, body := range map[string]string{
		"wrong password": `{"email":"lan@example.com","password":"not-the-password"}`,
		"unknown email":  `{"email":"nobody@example.com","password":"` + testPassword + `"}`,
		"inactive user":  `{"email":"off@example.com","password":"` + testPassword + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "INVALID_CREDENTIALS") {
				t.Fatalf("status = %d, body = %s; want 401 INVALID_CREDENTIALS", rec.Code, rec.Body.String())
			}
		})
	}
}

// unreachablePool is a Postgres pool whose server is down: every query fails with a connection error.
func unreachablePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), "postgres://iam_svc_user:x@127.0.0.1:1/commerce_lab?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestStoreOutageAnswers503InsteadOfSeededUser(t *testing.T) {
	srv := NewServer(repository.NewUserRepository(unreachablePool(t)), true)

	refreshToken, err := auth.SignRefreshToken("user-lan", "CUSTOMER", "NONE")
	if err != nil {
		t.Fatalf("sign refresh token: %v", err)
	}
	accessToken, err := auth.SignAccessToken("user-lan", "CUSTOMER", "NONE")
	if err != nil {
		t.Fatalf("sign access token: %v", err)
	}

	tests := []struct {
		name   string
		method string
		path   string
		body   string
		bearer string
	}{
		{name: "login", method: http.MethodPost, path: "/api/v1/auth/login", body: `{"email":"lan@example.com","password":"x"}`},
		{name: "refresh", method: http.MethodPost, path: "/auth/refresh", body: `{"refreshToken":"` + refreshToken + `"}`},
		{name: "me", method: http.MethodGet, path: "/api/v1/auth/me", bearer: accessToken},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			if tc.bearer != "" {
				req.Header.Set("Authorization", "Bearer "+tc.bearer)
			}
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)

			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503; body: %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "DEPENDENCY_UNAVAILABLE") {
				t.Fatalf("missing DEPENDENCY_UNAVAILABLE code: %s", rec.Body.String())
			}
		})
	}
}
