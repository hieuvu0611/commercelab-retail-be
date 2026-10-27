package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/services/iam-auth-service/internal/repository"
)

func TestAuthLoginMeAndRefreshFlow(t *testing.T) {
	srv := NewServer(repository.NewUserRepository(nil), false)

	// 1. Đăng nhập với tài khoản Nhân viên Kho HCM (I08: WarehouseScope = WH-HCM-01)
	loginBody := []byte(`{"email":"wh.hcm@commercelab.vn","password":"123456"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(loginBody))
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
