package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth/authtest"
	"github.com/hieuvu0611/commercelab-retail-be/services/iam-auth-service/internal/repository"
)

func postRegister(t *testing.T, srv http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func TestRegisterCreatesCustomerAndReturnsTokens(t *testing.T) {
	authtest.Configure(t)
	store := newFakeStore(t)
	srv := NewServer(store, true)

	rec := postRegister(t, srv, `{"email":"  New.User@Example.COM ","password":"a-long-enough-password","fullName":" New User "}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s; want 201", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data UserProfile `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Data.Email != "new.user@example.com" || resp.Data.Name != "New User" || resp.Data.Role != "CUSTOMER" {
		t.Fatalf("unexpected profile: %+v", resp.Data)
	}
	claims, err := auth.ParseAccessToken(resp.Data.AccessToken)
	if err != nil || claims.UserID != resp.Data.ID || claims.Role != "CUSTOMER" {
		t.Fatalf("access token does not match the new user: claims=%+v err=%v", claims, err)
	}
	if _, err := auth.ParseRefreshToken(resp.Data.RefreshToken); err != nil {
		t.Fatalf("refresh token invalid: %v", err)
	}
	if strings.Contains(rec.Body.String(), "argon2id") || strings.Contains(rec.Body.String(), "password_hash") {
		t.Fatalf("response must not contain password material: %s", rec.Body.String())
	}

	// The new account can sign in with the same password.
	loginReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login",
		strings.NewReader(`{"email":"new.user@example.com","password":"a-long-enough-password"}`))
	loginReq.Header.Set("Content-Type", "application/json")
	loginRec := httptest.NewRecorder()
	srv.ServeHTTP(loginRec, loginReq)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("login after register: %d %s", loginRec.Code, loginRec.Body.String())
	}
}

func TestRegisterRejectsDuplicateEmailCaseInsensitively(t *testing.T) {
	authtest.Configure(t)
	srv := NewServer(newFakeStore(t), true)

	rec := postRegister(t, srv, `{"email":"LAN@example.com","password":"a-long-enough-password","fullName":"Another Lan"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "EMAIL_ALREADY_REGISTERED") {
		t.Fatalf("status = %d, body = %s; want 409 EMAIL_ALREADY_REGISTERED", rec.Code, rec.Body.String())
	}
}

func TestRegisterValidatesInput(t *testing.T) {
	authtest.Configure(t)
	srv := NewServer(newFakeStore(t), true)

	tests := []struct {
		name      string
		body      string
		wantField string
	}{
		{"missing email", `{"password":"a-long-enough-password","fullName":"X"}`, "email"},
		{"malformed email", `{"email":"not-an-email","password":"a-long-enough-password","fullName":"X"}`, "email"},
		{"email with display name", `{"email":"Bob <bob@example.com>","password":"a-long-enough-password","fullName":"X"}`, "email"},
		{"short password", `{"email":"a@example.com","password":"short","fullName":"X"}`, "password"},
		{"nine character password", `{"email":"a@example.com","password":"123456789","fullName":"X"}`, "password"},
		{"password equals email", `{"email":"someone@example.com","password":"Someone@Example.com","fullName":"X"}`, "password"},
		{"password too long", `{"email":"a@example.com","password":"` + strings.Repeat("a", 129) + `","fullName":"X"}`, "password"},
		{"missing name", `{"email":"a@example.com","password":"a-long-enough-password"}`, "fullName"},
		{"blank name", `{"email":"a@example.com","password":"a-long-enough-password","fullName":"   "}`, "fullName"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := postRegister(t, srv, tc.body)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, body = %s; want 422", rec.Code, rec.Body.String())
			}
			var resp struct {
				Code   string              `json:"code"`
				Errors map[string][]string `json:"errors"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if resp.Code != "VALIDATION_FAILED" || len(resp.Errors[tc.wantField]) == 0 {
				t.Fatalf("want VALIDATION_FAILED with errors for %q, got %s", tc.wantField, rec.Body.String())
			}
		})
	}
}

func TestRegisterRejectsUnknownFieldsAndRoleEscalation(t *testing.T) {
	authtest.Configure(t)
	srv := NewServer(newFakeStore(t), true)

	rec := postRegister(t, srv, `{"email":"a@example.com","password":"a-long-enough-password","fullName":"X","role":"ADMIN"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s; want 400 for an unknown field", rec.Code, rec.Body.String())
	}
}

func TestRegisterAnswers503WhenStoreIsDown(t *testing.T) {
	authtest.Configure(t)
	srv := NewServer(repository.NewUserRepository(unreachablePool(t)), true)

	rec := postRegister(t, srv, `{"email":"a@example.com","password":"a-long-enough-password","fullName":"X"}`)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "DEPENDENCY_UNAVAILABLE") {
		t.Fatalf("status = %d, body = %s; want 503", rec.Code, rec.Body.String())
	}
}
