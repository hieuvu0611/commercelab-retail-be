package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth/authtest"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/services/iam-auth-service/internal/audit"
	"github.com/hieuvu0611/commercelab-retail-be/services/iam-auth-service/internal/ratelimit"
)

type recordingAudit struct {
	mu     sync.Mutex
	events []audit.Event
}

func (r *recordingAudit) Record(_ context.Context, e audit.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *recordingAudit) names() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.events))
	for _, e := range r.events {
		out = append(out, e.Name)
	}
	return out
}

func (r *recordingAudit) last() audit.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.events[len(r.events)-1]
}

type guardedServer struct {
	handler http.Handler
	clock   *time.Time
	audit   *recordingAudit
	limiter *ratelimit.Memory
}

func newGuardedServer(t *testing.T) *guardedServer {
	t.Helper()
	authtest.Configure(t)
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	g := &guardedServer{clock: &now, audit: &recordingAudit{}}
	g.limiter = ratelimit.NewMemory(ratelimit.MaxFailures, ratelimit.Window, func() time.Time { return *g.clock })
	g.handler = NewServerFromDeps(Deps{
		Users: newFakeStore(t), Tokens: newFakeTokens(), Limiter: g.limiter, Audit: g.audit, DBConnected: true,
	})
	return g
}

func (g *guardedServer) login(email, password, forwardedFor string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"email":"`+email+`","password":"`+password+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "guard-test")
	if forwardedFor != "" {
		req.Header.Set("X-Forwarded-For", forwardedFor)
	}
	rec := httptest.NewRecorder()
	g.handler.ServeHTTP(rec, req)
	return rec
}

func (g *guardedServer) failFiveTimes(email, ip string) {
	for i := 0; i < ratelimit.MaxFailures; i++ {
		g.login(email, "wrong-password-!", ip)
	}
}

func TestSixthLoginAttemptIsBlockedEvenWithTheCorrectPassword(t *testing.T) {
	g := newGuardedServer(t)
	g.failFiveTimes("lan@example.com", "203.0.113.7")

	rec := g.login("lan@example.com", testPassword, "203.0.113.7")
	if rec.Code != http.StatusTooManyRequests || !strings.Contains(rec.Body.String(), "TOO_MANY_ATTEMPTS") {
		t.Fatalf("status = %d, body = %s; want 429 TOO_MANY_ATTEMPTS", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("a 429 must carry Retry-After")
	}
	if last := g.audit.last(); last.Name != audit.LoginBlocked || last.Outcome != audit.OutcomeBlocked {
		t.Fatalf("last audit event = %+v, want LOGIN_BLOCKED", last)
	}
}

func TestBlockingIsPerEmailAndIPAndWindowed(t *testing.T) {
	g := newGuardedServer(t)
	g.failFiveTimes("lan@example.com", "203.0.113.7")

	if rec := g.login("lan@example.com", testPassword, "198.51.100.9"); rec.Code != http.StatusOK {
		t.Fatalf("another IP must not be blocked: %d %s", rec.Code, rec.Body.String())
	}
	if rec := g.login("wh.hcm@commercelab.vn", testPassword, "203.0.113.7"); rec.Code != http.StatusOK {
		t.Fatalf("another email from the same IP must not be blocked: %d %s", rec.Code, rec.Body.String())
	}

	*g.clock = g.clock.Add(ratelimit.Window + time.Second)
	if rec := g.login("lan@example.com", testPassword, "203.0.113.7"); rec.Code != http.StatusOK {
		t.Fatalf("after the window the user must be allowed again: %d %s", rec.Code, rec.Body.String())
	}
}

func TestSuccessfulLoginResetsTheFailureCount(t *testing.T) {
	g := newGuardedServer(t)
	for i := 0; i < ratelimit.MaxFailures-1; i++ {
		g.login("lan@example.com", "wrong-password-!", "203.0.113.7")
	}
	if rec := g.login("lan@example.com", testPassword, "203.0.113.7"); rec.Code != http.StatusOK {
		t.Fatalf("login: %d", rec.Code)
	}
	for i := 0; i < ratelimit.MaxFailures-1; i++ {
		if rec := g.login("lan@example.com", "wrong-password-!", "203.0.113.7"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d after the reset: %d; want 401", i+1, rec.Code)
		}
	}
}

func TestForgedForwardedForEntriesDoNotEvadeTheLimit(t *testing.T) {
	g := newGuardedServer(t)
	// The gateway appends the real client address as the last entry; an attacker controls the earlier ones.
	for i := 0; i < ratelimit.MaxFailures; i++ {
		g.login("lan@example.com", "wrong-password-!", "10.9.9."+string(rune('1'+i))+", 203.0.113.7")
	}
	if rec := g.login("lan@example.com", testPassword, "172.16.0.1, 203.0.113.7"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d; want 429 (the last forwarded address identifies the client)", rec.Code)
	}
}

func TestUnknownEmailsAreLimitedToo(t *testing.T) {
	g := newGuardedServer(t)
	g.failFiveTimes("nobody@example.com", "203.0.113.7")
	if rec := g.login("nobody@example.com", "whatever-password", "203.0.113.7"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d; want 429 for an email that does not exist as well", rec.Code)
	}
}

func TestLimiterOutageAnswers503(t *testing.T) {
	authtest.Configure(t)
	srv := NewServerFromDeps(Deps{
		Users: newFakeStore(t), Tokens: newFakeTokens(), Limiter: brokenLimiter{}, Audit: &recordingAudit{}, DBConnected: true,
	})
	rec := postJSON(t, srv, "/api/v1/auth/login", `{"email":"lan@example.com","password":"`+testPassword+`"}`)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "DEPENDENCY_UNAVAILABLE") {
		t.Fatalf("status = %d, body = %s; want 503", rec.Code, rec.Body.String())
	}
}

type brokenLimiter struct{}

func (brokenLimiter) Blocked(context.Context, string) (bool, time.Duration, error) {
	return false, 0, dependency.Wrap(errors.New("redis down"))
}
func (brokenLimiter) RecordFailure(context.Context, string) error {
	return dependency.Wrap(errors.New("redis down"))
}
func (brokenLimiter) Reset(context.Context, string) error {
	return dependency.Wrap(errors.New("redis down"))
}

func TestAuthEventsAreAuditedWithoutRawPersonalData(t *testing.T) {
	g := newGuardedServer(t)

	g.login("lan@example.com", "wrong-password-!", "203.0.113.7")
	g.login("nobody@example.com", "wrong-password-!", "203.0.113.7")
	loginRec := g.login("lan@example.com", testPassword, "203.0.113.7")
	var login struct {
		Data tokenPair `json:"data"`
	}
	_ = json.Unmarshal(loginRec.Body.Bytes(), &login)

	registerRec := postRegister(t, g.handler, `{"email":"fresh@example.com","password":"a-long-enough-password","fullName":"Fresh"}`)
	if registerRec.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", registerRec.Code, registerRec.Body.String())
	}
	postJSON(t, g.handler, "/api/v1/auth/logout", `{"refreshToken":"`+login.Data.RefreshToken+`"}`)

	want := []string{audit.LoginFailed, audit.LoginFailed, audit.LoginSuccess, audit.Registered, audit.Logout}
	got := g.audit.names()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("audit events = %v, want %v", got, want)
	}

	first := g.audit.events[0]
	if first.Outcome != audit.OutcomeFailure || first.IP != "203.0.113.7" || first.UserAgent != "guard-test" || first.UserID != "user-lan" {
		t.Fatalf("failed login event = %+v", first)
	}
	unknown := g.audit.events[1]
	if unknown.UserID != "" {
		t.Fatalf("a failed login for an unknown email has no user id, got %q", unknown.UserID)
	}
	for i, e := range g.audit.events {
		raw, _ := json.Marshal(e)
		for _, secret := range []string{"lan@example.com", "nobody@example.com", "fresh@example.com", testPassword, "a-long-enough-password", "wrong-password-!"} {
			if strings.Contains(string(raw), secret) {
				t.Fatalf("event %d (%s) contains personal data %q: %s", i, e.Name, secret, raw)
			}
		}
	}
	if got := unknown.Detail["email"]; got != "n***@example.com" {
		t.Fatalf("the redacted email detail = %v, want n***@example.com", got)
	}
}

func TestRefreshReuseAndPasswordChangeAreAudited(t *testing.T) {
	g := newGuardedServer(t)
	first := loginAs(t, g.handler)
	second := decodePair(t, refreshWith(t, g.handler, "/api/v1/auth/refresh", first.RefreshToken))
	_ = second

	refreshWith(t, g.handler, "/api/v1/auth/refresh", first.RefreshToken) // reuse of the rotated token
	if last := g.audit.last(); last.Name != audit.RefreshReuseDetected || last.UserID != "user-lan" || last.Outcome != audit.OutcomeFailure {
		t.Fatalf("last audit event = %+v, want REFRESH_REUSE_DETECTED for user-lan", last)
	}

	login := loginAs(t, g.handler)
	rec := authedRequest(t, g.handler, http.MethodPost, "/api/v1/auth/change-password", login.AccessToken,
		`{"currentPassword":"`+testPassword+`","newPassword":"a-brand-new-password"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("change password: %d %s", rec.Code, rec.Body.String())
	}
	if last := g.audit.last(); last.Name != audit.PasswordChanged || last.UserID != "user-lan" {
		t.Fatalf("last audit event = %+v, want PASSWORD_CHANGED", last)
	}
}

func TestWrongCurrentPasswordAttemptsAreLimited(t *testing.T) {
	g := newGuardedServer(t)
	login := loginAs(t, g.handler)
	body := `{"currentPassword":"not-my-password","newPassword":"a-brand-new-password"}`
	for i := 0; i < ratelimit.MaxFailures; i++ {
		if rec := authedRequest(t, g.handler, http.MethodPost, "/api/v1/auth/change-password", login.AccessToken, body); rec.Code != http.StatusForbidden {
			t.Fatalf("attempt %d: %d; want 403", i+1, rec.Code)
		}
	}
	rec := authedRequest(t, g.handler, http.MethodPost, "/api/v1/auth/change-password", login.AccessToken,
		`{"currentPassword":"`+testPassword+`","newPassword":"a-brand-new-password"}`)
	if rec.Code != http.StatusTooManyRequests || !strings.Contains(rec.Body.String(), "TOO_MANY_ATTEMPTS") {
		t.Fatalf("status = %d, body = %s; want 429 even with the right password", rec.Code, rec.Body.String())
	}
}

func TestClientIPUsesTheLastForwardedAddress(t *testing.T) {
	tests := []struct {
		name, xff, remote, want string
	}{
		{"no header uses the peer", "", "192.0.2.10:5555", "192.0.2.10"},
		{"single entry", "203.0.113.7", "10.0.0.1:1", "203.0.113.7"},
		{"last entry wins", "1.1.1.1, 2.2.2.2, 203.0.113.7", "10.0.0.1:1", "203.0.113.7"},
		{"blank entries are ignored", "203.0.113.7, ", "10.0.0.1:1", "203.0.113.7"},
		{"peer without a port", "", "192.0.2.10", "192.0.2.10"},
	}
	for _, tc := range tests {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = tc.remote
		if tc.xff != "" {
			req.Header.Set("X-Forwarded-For", tc.xff)
		}
		if got := clientIP(req); got != tc.want {
			t.Errorf("%s: clientIP = %q, want %q", tc.name, got, tc.want)
		}
	}
}
