package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth/authtest"
)

func authedRequest(t *testing.T, srv http.Handler, method, path, bearer, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

type profileResponse struct {
	Data UserProfile `json:"data"`
}

func decodeProfile(t *testing.T, rec *httptest.ResponseRecorder) profileResponse {
	t.Helper()
	var resp profileResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	return resp
}

func TestMeRequiresAValidAccessToken(t *testing.T) {
	authtest.Configure(t)
	srv := newTestServer(t)
	for _, method := range []string{http.MethodGet, http.MethodPatch} {
		if rec := authedRequest(t, srv, method, "/api/v1/auth/me", "", `{}`); rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s /me without a token: %d; want 401", method, rec.Code)
		}
	}
	if rec := authedRequest(t, srv, http.MethodPost, "/api/v1/auth/change-password", "", `{}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("change-password without a token: %d; want 401", rec.Code)
	}
	login := loginAs(t, srv)
	if rec := authedRequest(t, srv, http.MethodGet, "/api/v1/auth/me", login.RefreshToken, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("a refresh token must not open /me: %d", rec.Code)
	}
}

func TestPatchMeUpdatesNameAndPhoneAndNothingElse(t *testing.T) {
	authtest.Configure(t)
	srv := newTestServer(t)
	login := loginAs(t, srv)

	rec := authedRequest(t, srv, http.MethodPatch, "/api/v1/auth/me", login.AccessToken, `{"fullName":"  Lan Nguyen ","phone":"+84 (90) 123-4567"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", rec.Code, rec.Body.String())
	}
	resp := decodeProfile(t, rec)
	if resp.Data.Name != "Lan Nguyen" || resp.Data.Phone != "+84901234567" {
		t.Fatalf("profile = %+v; want trimmed name and a normalized phone", resp.Data)
	}
	if resp.Data.Role != "CUSTOMER" || resp.Data.Email != "lan@example.com" {
		t.Fatalf("role/email must not change: %+v", resp.Data)
	}

	got := decodeProfile(t, authedRequest(t, srv, http.MethodGet, "/api/v1/auth/me", login.AccessToken, ""))
	if got.Data.Name != "Lan Nguyen" || got.Data.Phone != "+84901234567" {
		t.Fatalf("GET /me after patch = %+v", got.Data)
	}

	// Only the phone is sent: the name stays. An empty phone clears it.
	authedRequest(t, srv, http.MethodPatch, "/api/v1/auth/me", login.AccessToken, `{"phone":""}`)
	got = decodeProfile(t, authedRequest(t, srv, http.MethodGet, "/api/v1/auth/me", login.AccessToken, ""))
	if got.Data.Name != "Lan Nguyen" || got.Data.Phone != "" {
		t.Fatalf("after clearing the phone: %+v", got.Data)
	}
}

func TestPatchMeRejectsBadInput(t *testing.T) {
	authtest.Configure(t)
	srv := newTestServer(t)
	login := loginAs(t, srv)

	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantField  string
	}{
		{"empty body", `{}`, http.StatusUnprocessableEntity, "fullName"},
		{"blank name", `{"fullName":"   "}`, http.StatusUnprocessableEntity, "fullName"},
		{"name too long", `{"fullName":"` + strings.Repeat("a", 101) + `"}`, http.StatusUnprocessableEntity, "fullName"},
		{"phone with letters", `{"phone":"call-me-maybe"}`, http.StatusUnprocessableEntity, "phone"},
		{"phone too short", `{"phone":"12345"}`, http.StatusUnprocessableEntity, "phone"},
		{"role escalation", `{"role":"ACCESS_ADMIN"}`, http.StatusBadRequest, ""},
		{"email change", `{"email":"x@example.com"}`, http.StatusBadRequest, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := authedRequest(t, srv, http.MethodPatch, "/api/v1/auth/me", login.AccessToken, tc.body)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, body = %s; want %d", rec.Code, rec.Body.String(), tc.wantStatus)
			}
			if tc.wantField != "" && !strings.Contains(rec.Body.String(), `"`+tc.wantField+`"`) {
				t.Fatalf("body must name the field %s: %s", tc.wantField, rec.Body.String())
			}
		})
	}
}

func TestChangePasswordRevokesOtherSessionsAndKeepsThisOne(t *testing.T) {
	authtest.Configure(t)
	srv := newTestServer(t)
	thisDevice := loginAs(t, srv)
	otherDevice := loginAs(t, srv)

	rec := authedRequest(t, srv, http.MethodPost, "/api/v1/auth/change-password", thisDevice.AccessToken,
		`{"currentPassword":"`+testPassword+`","newPassword":"a-brand-new-password"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("change password: %d %s", rec.Code, rec.Body.String())
	}
	fresh := decodePair(t, rec)
	if fresh.AccessToken == "" || fresh.RefreshToken == "" {
		t.Fatalf("this device must receive a fresh session: %s", rec.Body.String())
	}

	// Every earlier refresh token is dead, on this device and on the other one.
	for name, token := range map[string]string{"this device (old)": thisDevice.RefreshToken, "other device": otherDevice.RefreshToken} {
		if rec := refreshWith(t, srv, "/api/v1/auth/refresh", token); rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s must be signed out: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	// The fresh session works.
	if rec := refreshWith(t, srv, "/api/v1/auth/refresh", fresh.RefreshToken); rec.Code != http.StatusOK {
		t.Fatalf("the fresh session must work: %d %s", rec.Code, rec.Body.String())
	}

	// The new password signs in; the old one no longer does.
	if rec := postJSON(t, srv, "/api/v1/auth/login", `{"email":"lan@example.com","password":"a-brand-new-password"}`); rec.Code != http.StatusOK {
		t.Fatalf("login with the new password: %d %s", rec.Code, rec.Body.String())
	}
	if rec := postJSON(t, srv, "/api/v1/auth/login", `{"email":"lan@example.com","password":"`+testPassword+`"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("login with the old password: %d; want 401", rec.Code)
	}
}

func TestChangePasswordChecksCurrentPasswordAndPolicy(t *testing.T) {
	authtest.Configure(t)
	srv := newTestServer(t)
	login := loginAs(t, srv)

	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantCode   string
	}{
		{"wrong current password", `{"currentPassword":"not-my-password","newPassword":"a-brand-new-password"}`, http.StatusForbidden, "INVALID_CURRENT_PASSWORD"},
		{"missing current password", `{"newPassword":"a-brand-new-password"}`, http.StatusUnprocessableEntity, "VALIDATION_FAILED"},
		{"short new password", `{"currentPassword":"` + testPassword + `","newPassword":"short"}`, http.StatusUnprocessableEntity, "VALIDATION_FAILED"},
		{"new password equals email", `{"currentPassword":"` + testPassword + `","newPassword":"lan@example.com"}`, http.StatusUnprocessableEntity, "VALIDATION_FAILED"},
		{"new password equals current", `{"currentPassword":"` + testPassword + `","newPassword":"` + testPassword + `"}`, http.StatusUnprocessableEntity, "VALIDATION_FAILED"},
		{"unknown field", `{"currentPassword":"` + testPassword + `","newPassword":"a-brand-new-password","userId":"x"}`, http.StatusBadRequest, "INVALID_JSON"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := authedRequest(t, srv, http.MethodPost, "/api/v1/auth/change-password", login.AccessToken, tc.body)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantCode) {
				t.Fatalf("status = %d, body = %s; want %d %s", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantCode)
			}
		})
	}
	// Nothing above may have revoked the session.
	if rec := refreshWith(t, srv, "/api/v1/auth/refresh", login.RefreshToken); rec.Code != http.StatusOK {
		t.Fatalf("a rejected change must leave sessions alone: %d %s", rec.Code, rec.Body.String())
	}
}
