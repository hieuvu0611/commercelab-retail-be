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
	"github.com/hieuvu0611/commercelab-retail-be/services/iam-auth-service/internal/repository"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAuthLoginMeAndRefreshFlow(t *testing.T) {
	srv := NewServer(repository.NewUserRepository(nil), false)

	// 1. Đăng nhập với tài khoản Nhân viên Kho HCM (I08: WarehouseScope = WH-HCM-01)
	loginBody := []byte(`{"email":"wh.hcm@commercelab.vn","password":"123456"}`)
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
	srv := NewServer(repository.NewUserRepository(nil), false)
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
		{name: "login", method: http.MethodPost, path: "/api/v1/auth/login", body: `{"email":"lan@example.com","password":"123456"}`},
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
