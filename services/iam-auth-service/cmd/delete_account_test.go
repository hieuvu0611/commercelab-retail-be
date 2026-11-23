package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth/authtest"
	"github.com/hieuvu0611/commercelab-retail-be/services/iam-auth-service/internal/audit"
	"github.com/hieuvu0611/commercelab-retail-be/services/iam-auth-service/internal/ratelimit"
	"github.com/hieuvu0611/commercelab-retail-be/services/iam-auth-service/internal/repository"
)

type deleteAccountFixture struct {
	handler   http.Handler
	store     *fakeStore
	tokens    *fakeTokens
	addresses *repository.MemoryAddressStore
	audit     *recordingAudit
	clock     *time.Time
}

func newDeleteAccountFixture(t *testing.T) *deleteAccountFixture {
	t.Helper()
	authtest.Configure(t)
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	tokens := newFakeTokens()
	addresses := repository.NewMemoryAddressStore()
	store := newFakeStore(t)
	store.tokens = tokens
	store.addresses = addresses
	aud := &recordingAudit{}
	limiter := ratelimit.NewMemory(ratelimit.MaxFailures, ratelimit.Window, func() time.Time { return now })
	handler := NewServerFromDeps(Deps{
		Users:       store,
		Tokens:      tokens,
		Addresses:   addresses,
		Limiter:     limiter,
		Audit:       aud,
		DBConnected: true,
	})
	return &deleteAccountFixture{
		handler:   handler,
		store:     store,
		tokens:    tokens,
		addresses: addresses,
		audit:     aud,
		clock:     &now,
	}
}

func TestDeleteAccountCustomerHappyPathScrubsPIIAndFreesEmail(t *testing.T) {
	fx := newDeleteAccountFixture(t)
	session := loginAs(t, fx.handler)

	patchRec := authedRequest(t, fx.handler, http.MethodPatch, "/api/v1/auth/me", session.AccessToken,
		`{"fullName":"Lan Nguyen","phone":"+84901234567"}`)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("patch profile: %d %s", patchRec.Code, patchRec.Body.String())
	}
	addrRec := authedRequest(t, fx.handler, http.MethodPost, "/api/v1/users/me/addresses", session.AccessToken, addressBody)
	if addrRec.Code != http.StatusCreated {
		t.Fatalf("create address: %d %s", addrRec.Code, addrRec.Body.String())
	}

	delRec := authedRequest(t, fx.handler, http.MethodPost, "/api/v1/auth/delete-account", session.AccessToken,
		`{"currentPassword":"`+testPassword+`","confirmation":"DELETE"}`)
	if delRec.Code != http.StatusOK {
		t.Fatalf("delete account: %d %s", delRec.Code, delRec.Body.String())
	}
	var delResp struct {
		Data struct {
			UserID         string `json:"userId"`
			Anonymized     bool   `json:"anonymized"`
			ErasedAt       string `json:"erasedAt"`
			RetentionUntil string `json:"retentionUntil"`
		} `json:"data"`
	}
	if err := json.Unmarshal(delRec.Body.Bytes(), &delResp); err != nil {
		t.Fatalf("decode delete response: %v (%s)", err, delRec.Body.String())
	}
	if delResp.Data.UserID != "user-lan" || !delResp.Data.Anonymized {
		t.Fatalf("unexpected delete response: %+v", delResp.Data)
	}
	erasedAt, err := time.Parse(time.RFC3339, delResp.Data.ErasedAt)
	if err != nil {
		t.Fatalf("invalid erasedAt %q: %v", delResp.Data.ErasedAt, err)
	}
	retentionUntil, err := time.Parse(time.RFC3339, delResp.Data.RetentionUntil)
	if err != nil {
		t.Fatalf("invalid retentionUntil %q: %v", delResp.Data.RetentionUntil, err)
	}
	if want := erasedAt.AddDate(repository.FinancialRetentionYears, 0, 0); !retentionUntil.Equal(want) {
		t.Fatalf("retentionUntil = %s, want %s", retentionUntil, want)
	}

	stored, err := fx.store.FindByID(context.Background(), "user-lan")
	if err != nil {
		t.Fatalf("find deleted user: %v", err)
	}
	if stored.Email != repository.AnonymizedEmail("user-lan") || stored.Name != "Deleted User" ||
		stored.Phone != "" || stored.IsActive || stored.DeletedAt == nil || stored.AnonymizedAt == nil {
		t.Fatalf("stored user was not properly anonymized: %+v", stored)
	}

	addrs, err := fx.addresses.List(context.Background(), "user-lan")
	if err != nil || len(addrs) != 0 {
		t.Fatalf("expected 0 addresses after deletion, got %d (err=%v)", len(addrs), err)
	}

	if rec := refreshWith(t, fx.handler, "/api/v1/auth/refresh", session.RefreshToken); rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "USER_INACTIVE") {
		t.Fatalf("refresh after deletion: %d %s; want 401 USER_INACTIVE", rec.Code, rec.Body.String())
	}
	fx.tokens.mu.Lock()
	for _, tok := range fx.tokens.byHash {
		if tok.UserID == "user-lan" && !fx.tokens.revoked[tok.ID] {
			fx.tokens.mu.Unlock()
			t.Fatalf("refresh token %s was not revoked", tok.ID)
		}
	}
	fx.tokens.mu.Unlock()

	if rec := postJSON(t, fx.handler, "/api/v1/auth/login", `{"email":"lan@example.com","password":"`+testPassword+`"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("login after deletion: %d %s; want 401", rec.Code, rec.Body.String())
	}

	blockedCalls := []struct {
		name, method, path, body string
	}{
		{"GET /me", http.MethodGet, "/api/v1/auth/me", ""},
		{"PATCH /me", http.MethodPatch, "/api/v1/auth/me", `{"fullName":"Resurrected"}`},
		{"POST /change-password", http.MethodPost, "/api/v1/auth/change-password", `{"currentPassword":"` + testPassword + `","newPassword":"a-brand-new-password"}`},
		{"second POST /delete-account", http.MethodPost, "/api/v1/auth/delete-account", `{"currentPassword":"` + testPassword + `","confirmation":"DELETE"}`},
	}
	for _, tc := range blockedCalls {
		rec := authedRequest(t, fx.handler, tc.method, tc.path, session.AccessToken, tc.body)
		if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "USER_INACTIVE") {
			t.Fatalf("%s after deletion: %d %s; want 401 USER_INACTIVE", tc.name, rec.Code, rec.Body.String())
		}
	}

	regRec := postRegister(t, fx.handler, `{"email":"lan@example.com","password":"a-brand-new-password","fullName":"Lan Reborn"}`)
	if regRec.Code != http.StatusCreated {
		t.Fatalf("re-register with freed email: %d %s; want 201", regRec.Code, regRec.Body.String())
	}
	var regResp struct {
		Data UserProfile `json:"data"`
	}
	if err := json.Unmarshal(regRec.Body.Bytes(), &regResp); err != nil {
		t.Fatalf("decode re-register: %v", err)
	}
	if regResp.Data.ID == "user-lan" || regResp.Data.Email != "lan@example.com" {
		t.Fatalf("expected fresh user ID for re-registered email, got %+v", regResp.Data)
	}
}

func TestDeleteAccountWrongPasswordReturns403AndPreservesSessions(t *testing.T) {
	fx := newDeleteAccountFixture(t)
	session := loginAs(t, fx.handler)

	rec := authedRequest(t, fx.handler, http.MethodPost, "/api/v1/auth/delete-account", session.AccessToken,
		`{"currentPassword":"wrong-password","confirmation":"DELETE"}`)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "INVALID_CURRENT_PASSWORD") {
		t.Fatalf("wrong password: %d %s; want 403 INVALID_CURRENT_PASSWORD", rec.Code, rec.Body.String())
	}

	if r := refreshWith(t, fx.handler, "/api/v1/auth/refresh", session.RefreshToken); r.Code != http.StatusOK {
		t.Fatalf("refresh after wrong password: %d %s; want 200", r.Code, r.Body.String())
	}
	if r := authedRequest(t, fx.handler, http.MethodGet, "/api/v1/auth/me", session.AccessToken, ""); r.Code != http.StatusOK {
		t.Fatalf("GET /me after wrong password: %d %s; want 200", r.Code, r.Body.String())
	}
	if last := fx.audit.last(); last.Name != audit.AccountDeleteFailed || last.Outcome != audit.OutcomeFailure || last.UserID != "user-lan" {
		t.Fatalf("last audit event = %+v; want ACCOUNT_DELETE_FAILED failure for user-lan", last)
	}
}

func TestDeleteAccountValidationRejectsWrongConfirmationAndMissingFields(t *testing.T) {
	fx := newDeleteAccountFixture(t)
	session := loginAs(t, fx.handler)

	cases := []struct {
		name       string
		body       string
		wantStatus int
		wantCode   string
		wantField  string
	}{
		{"wrong confirmation", `{"currentPassword":"` + testPassword + `","confirmation":"delete"}`, http.StatusUnprocessableEntity, "VALIDATION_FAILED", "confirmation"},
		{"empty confirmation", `{"currentPassword":"` + testPassword + `","confirmation":""}`, http.StatusUnprocessableEntity, "VALIDATION_FAILED", "confirmation"},
		{"missing currentPassword", `{"confirmation":"DELETE"}`, http.StatusUnprocessableEntity, "VALIDATION_FAILED", "currentPassword"},
		{"unknown field", `{"currentPassword":"` + testPassword + `","confirmation":"DELETE","extra":true}`, http.StatusBadRequest, "INVALID_JSON", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := authedRequest(t, fx.handler, http.MethodPost, "/api/v1/auth/delete-account", session.AccessToken, tc.body)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantCode) {
				t.Fatalf("status = %d, body = %s; want %d %s", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantCode)
			}
			if tc.wantField != "" && !strings.Contains(rec.Body.String(), `"`+tc.wantField+`"`) {
				t.Fatalf("expected field error %q in %s", tc.wantField, rec.Body.String())
			}
		})
	}
}

func TestDeleteAccountBlocksStaffSelfDeletion(t *testing.T) {
	fx := newDeleteAccountFixture(t)
	loginRec := postJSON(t, fx.handler, "/api/v1/auth/login", `{"email":"wh.hcm@commercelab.vn","password":"`+testPassword+`"}`)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("staff login: %d %s", loginRec.Code, loginRec.Body.String())
	}
	staffSession := decodePair(t, loginRec)

	rec := authedRequest(t, fx.handler, http.MethodPost, "/api/v1/auth/delete-account", staffSession.AccessToken,
		`{"currentPassword":"`+testPassword+`","confirmation":"DELETE"}`)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "STAFF_ACCOUNT_CANNOT_SELF_DELETE") {
		t.Fatalf("staff self-delete: %d %s; want 403 STAFF_ACCOUNT_CANNOT_SELF_DELETE", rec.Code, rec.Body.String())
	}
	if r := authedRequest(t, fx.handler, http.MethodGet, "/api/v1/auth/me", staffSession.AccessToken, ""); r.Code != http.StatusOK {
		t.Fatalf("staff account must remain active: %d %s", r.Code, r.Body.String())
	}
}

func TestDeleteAccountRequiresAuthentication(t *testing.T) {
	fx := newDeleteAccountFixture(t)
	body := `{"currentPassword":"` + testPassword + `","confirmation":"DELETE"}`

	if rec := authedRequest(t, fx.handler, http.MethodPost, "/api/v1/auth/delete-account", "", body); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous delete-account: %d; want 401", rec.Code)
	}
	session := loginAs(t, fx.handler)
	if rec := authedRequest(t, fx.handler, http.MethodPost, "/api/v1/auth/delete-account", session.RefreshToken, body); rec.Code != http.StatusUnauthorized {
		t.Fatalf("refresh token on delete-account: %d; want 401", rec.Code)
	}
}

func TestDeleteAccountRateLimitsFailedAttempts(t *testing.T) {
	fx := newDeleteAccountFixture(t)
	session := loginAs(t, fx.handler)
	wrongBody := `{"currentPassword":"wrong-password","confirmation":"DELETE"}`

	for i := 0; i < ratelimit.MaxFailures; i++ {
		rec := authedRequest(t, fx.handler, http.MethodPost, "/api/v1/auth/delete-account", session.AccessToken, wrongBody)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("attempt %d: %d %s; want 403", i+1, rec.Code, rec.Body.String())
		}
	}

	rec := authedRequest(t, fx.handler, http.MethodPost, "/api/v1/auth/delete-account", session.AccessToken,
		`{"currentPassword":"`+testPassword+`","confirmation":"DELETE"}`)
	if rec.Code != http.StatusTooManyRequests || !strings.Contains(rec.Body.String(), "TOO_MANY_ATTEMPTS") {
		t.Fatalf("rate-limited delete-account: %d %s; want 429 TOO_MANY_ATTEMPTS", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("429 response must include Retry-After header")
	}
	if last := fx.audit.last(); last.Name != audit.AccountDeleteFailed || last.Outcome != audit.OutcomeBlocked || last.UserID != "user-lan" {
		t.Fatalf("last audit event = %+v; want ACCOUNT_DELETE_FAILED blocked for user-lan", last)
	}
}

func TestDeleteAccountReturns503OnStoreOutage(t *testing.T) {
	authtest.Configure(t)
	srv := newOutageServer(t)
	tok, err := auth.SignAccessToken(auth.Identity{UserID: "user-lan", Role: "CUSTOMER", WarehouseScope: "NONE", Roles: []string{"CUSTOMER"}})
	if err != nil {
		t.Fatalf("sign access token: %v", err)
	}
	rec := authedRequest(t, srv, http.MethodPost, "/api/v1/auth/delete-account", tok,
		`{"currentPassword":"`+testPassword+`","confirmation":"DELETE"}`)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "DEPENDENCY_UNAVAILABLE") {
		t.Fatalf("outage delete-account: %d %s; want 503 DEPENDENCY_UNAVAILABLE", rec.Code, rec.Body.String())
	}
}
