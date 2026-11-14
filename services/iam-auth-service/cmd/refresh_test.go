package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth/authtest"
)

func postJSON(t *testing.T, srv http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

type tokenPair struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
}

func loginAs(t *testing.T, srv http.Handler) tokenPair {
	t.Helper()
	rec := postJSON(t, srv, "/api/v1/auth/login", `{"email":"lan@example.com","password":"`+testPassword+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data tokenPair `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode login: %v", err)
	}
	return resp.Data
}

func refreshWith(t *testing.T, srv http.Handler, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	return postJSON(t, srv, path, `{"refreshToken":"`+token+`"}`)
}

func decodePair(t *testing.T, rec *httptest.ResponseRecorder) tokenPair {
	t.Helper()
	var resp struct {
		Data tokenPair `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	return resp.Data
}

func TestRefreshRotatesTheToken(t *testing.T) {
	authtest.Configure(t)
	srv := newTestServer(t)
	first := loginAs(t, srv)

	rec := refreshWith(t, srv, "/api/v1/auth/refresh", first.RefreshToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh: %d %s", rec.Code, rec.Body.String())
	}
	second := decodePair(t, rec)
	if second.RefreshToken == "" || second.RefreshToken == first.RefreshToken {
		t.Fatal("refresh must return a new refresh token")
	}
	if _, err := auth.ParseAccessToken(second.AccessToken); err != nil {
		t.Fatalf("new access token invalid: %v", err)
	}
	if rec := refreshWith(t, srv, "/api/v1/auth/refresh", second.RefreshToken); rec.Code != http.StatusOK {
		t.Fatalf("the rotated token must work once: %d %s", rec.Code, rec.Body.String())
	}
}

func TestReusingARotatedRefreshTokenRevokesTheFamily(t *testing.T) {
	authtest.Configure(t)
	srv := newTestServer(t)
	first := loginAs(t, srv)
	second := decodePair(t, refreshWith(t, srv, "/api/v1/auth/refresh", first.RefreshToken))

	rec := refreshWith(t, srv, "/api/v1/auth/refresh", first.RefreshToken)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "REFRESH_TOKEN_REUSED") {
		t.Fatalf("reuse: %d %s; want 401 REFRESH_TOKEN_REUSED", rec.Code, rec.Body.String())
	}
	if rec := refreshWith(t, srv, "/api/v1/auth/refresh", second.RefreshToken); rec.Code != http.StatusUnauthorized {
		t.Fatalf("the successor of a reused token must be dead too: %d %s", rec.Code, rec.Body.String())
	}
}

func TestDeprecatedRefreshAliasStillWorksAndSaysSo(t *testing.T) {
	authtest.Configure(t)
	srv := newTestServer(t)
	first := loginAs(t, srv)

	rec := refreshWith(t, srv, "/auth/refresh", first.RefreshToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("alias: %d %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Deprecation") != "true" {
		t.Fatalf("alias must carry Deprecation: true, headers = %v", rec.Header())
	}
}

func TestRefreshRejectsBadTokens(t *testing.T) {
	authtest.Configure(t)
	srv := newTestServer(t)
	login := loginAs(t, srv)

	tests := map[string]string{
		"empty":                   "",
		"garbage":                 "not-a-jwt",
		"access token as refresh": login.AccessToken,
	}
	for name, token := range tests {
		t.Run(name, func(t *testing.T) {
			rec := refreshWith(t, srv, "/api/v1/auth/refresh", token)
			if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "INVALID_REFRESH_TOKEN") {
				t.Fatalf("%d %s; want 401 INVALID_REFRESH_TOKEN", rec.Code, rec.Body.String())
			}
		})
	}

	// A correctly signed token the server never stored (for example from before a database reset).
	unstored, err := auth.SignRefreshToken("user-lan", "CUSTOMER", "NONE", "rt-unknown")
	if err != nil {
		t.Fatal(err)
	}
	if rec := refreshWith(t, srv, "/api/v1/auth/refresh", unstored); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unstored token: %d %s; want 401", rec.Code, rec.Body.String())
	}
}

func TestExpiredStoredTokenIsRejected(t *testing.T) {
	authtest.Configure(t)
	tokens := newFakeTokens()
	srv := NewServer(newFakeStore(t), tokens, true)
	first := loginAs(t, srv)
	for _, rec := range tokens.byHash {
		tokens.expired[rec.ID] = true
	}

	rec := refreshWith(t, srv, "/api/v1/auth/refresh", first.RefreshToken)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "REFRESH_TOKEN_EXPIRED") {
		t.Fatalf("%d %s; want 401 REFRESH_TOKEN_EXPIRED", rec.Code, rec.Body.String())
	}
}

func TestLogoutRevokesTheFamilyAndIsIdempotent(t *testing.T) {
	authtest.Configure(t)
	srv := newTestServer(t)
	first := loginAs(t, srv)
	second := decodePair(t, refreshWith(t, srv, "/api/v1/auth/refresh", first.RefreshToken))

	if rec := postJSON(t, srv, "/api/v1/auth/logout", `{"refreshToken":"`+second.RefreshToken+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("logout: %d %s", rec.Code, rec.Body.String())
	}
	if rec := refreshWith(t, srv, "/api/v1/auth/refresh", second.RefreshToken); rec.Code != http.StatusUnauthorized {
		t.Fatalf("a logged-out token must not refresh: %d %s", rec.Code, rec.Body.String())
	}
	if rec := postJSON(t, srv, "/api/v1/auth/logout", `{"refreshToken":"`+second.RefreshToken+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("a second logout must also succeed: %d %s", rec.Code, rec.Body.String())
	}
	if rec := postJSON(t, srv, "/api/v1/auth/logout", `{"refreshToken":""}`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("logout without a token: %d %s; want 422", rec.Code, rec.Body.String())
	}
}

func TestRefreshOfADeactivatedUserIsRejected(t *testing.T) {
	authtest.Configure(t)
	store := newFakeStore(t)
	srv := NewServer(store, newFakeTokens(), true)
	first := loginAs(t, srv)

	u := store.byID["user-lan"]
	u.IsActive = false
	store.byID["user-lan"] = u

	rec := refreshWith(t, srv, "/api/v1/auth/refresh", first.RefreshToken)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "USER_INACTIVE") {
		t.Fatalf("%d %s; want 401 USER_INACTIVE", rec.Code, rec.Body.String())
	}
}

func TestSessionEndpointsAnswer503WhenStoreIsDown(t *testing.T) {
	authtest.Configure(t)
	srv := newOutageServer(t)
	token, err := auth.SignRefreshToken("user-lan", "CUSTOMER", "NONE", "rt-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/auth/refresh", "/api/v1/auth/logout"} {
		rec := postJSON(t, srv, path, `{"refreshToken":"`+token+`"}`)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s: %d %s; want 503", path, rec.Code, rec.Body.String())
		}
	}
}
