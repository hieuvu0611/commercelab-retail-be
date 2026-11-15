package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth/authtest"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/internalauth"
	"github.com/hieuvu0611/commercelab-retail-be/services/payment-service/internal/attempt"
	"github.com/hieuvu0611/commercelab-retail-be/services/payment-service/internal/domain"
)

const (
	internalToken = "test-internal-token-0123456789abcdef"
	webhookSecret = "test-webhook-secret-0123456789abcdef"
)

type fixture struct {
	srv   http.Handler
	store *attempt.MemoryStore
	clock *time.Time
}

func newFixture(t *testing.T, simulator bool) *fixture {
	t.Helper()
	authtest.Configure(t)
	_ = internalauth.Configure(internalToken)
	t.Cleanup(internalauth.Reset)
	now := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	f := &fixture{clock: &now}
	f.store = attempt.NewMemoryStore().WithClock(func() time.Time { return *f.clock })
	f.srv = NewServer(domain.NewPaymentEngine(), f.store, Options{SimulatorEnabled: simulator, WebhookSecret: webhookSecret})
	return f
}

// token signs a customer access token (authtest.Configure must have run).
func token(user string) string {
	tok, err := auth.SignAccessToken(auth.Identity{UserID: user, Roles: []string{"CUSTOMER"}})
	if err != nil {
		panic(err)
	}
	return tok
}

func (f *fixture) do(method, path string, headers map[string]string, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, req)
	return rec
}

func internalHeaders(key string) map[string]string {
	return map[string]string{internalauth.Header: internalToken, "Idempotency-Key": key}
}

func (f *fixture) createAttempt(key, order, user string, amount int64) *httptest.ResponseRecorder {
	return f.do(http.MethodPost, "/api/v1/payments/attempts", internalHeaders(key),
		fmt.Sprintf(`{"orderId":%q,"userId":%q,"amountVnd":%d}`, order, user, amount))
}

type attemptResp struct {
	Data struct {
		AttemptID         string `json:"attemptId"`
		OrderID           string `json:"orderId"`
		AmountVND         int64  `json:"amountVnd"`
		CapturedAmountVND int64  `json:"capturedAmountVnd"`
		Status            string `json:"status"`
		UnknownDeadline   string `json:"unknownDeadline"`
		UserID            string `json:"userId"`
	} `json:"data"`
}

type errResp struct {
	Code   string              `json:"code"`
	Status int                 `json:"status"`
	Errors map[string][]string `json:"errors"`
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return out
}

func sign(body string) string {
	mac := hmac.New(sha256.New, []byte(webhookSecret))
	mac.Write([]byte(body))
	return hex.EncodeToString(mac.Sum(nil))
}

func TestCreateAttemptIsInternalOnly(t *testing.T) {
	f := newFixture(t, true)
	body := `{"orderId":"O-1","userId":"user-lan","amountVnd":660000}`
	for name, h := range map[string]map[string]string{
		"anonymous":      {"Idempotency-Key": "k"},
		"customer token": {"Authorization": "Bearer " + token("user-lan"), "Idempotency-Key": "k"},
		"wrong secret":   {internalauth.Header: "guess", "Idempotency-Key": "k"},
	} {
		rec := f.do(http.MethodPost, "/api/v1/payments/attempts", h, body)
		if rec.Code != http.StatusUnauthorized || decode[errResp](t, rec).Code != "INTERNAL_AUTH_REQUIRED" {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
}

func TestCreateAttemptIsIdempotentAndOnePerOrder(t *testing.T) {
	f := newFixture(t, true)
	first := f.createAttempt("pay-O-1", "O-1", "user-lan", 660000)
	got := decode[attemptResp](t, first)
	if first.Code != http.StatusCreated || got.Data.AttemptID != "PAY-O-1" || got.Data.Status != "PROCESSING" || got.Data.AmountVND != 660000 || got.Data.UserID != "" {
		t.Fatalf("create = %d %s", first.Code, first.Body.String())
	}
	if again := f.createAttempt("pay-O-1", "O-1", "user-lan", 660000); again.Code != http.StatusOK {
		t.Fatalf("replay = %d %s", again.Code, again.Body.String())
	}
	if rec := f.createAttempt("pay-O-1", "O-1", "user-lan", 100); rec.Code != http.StatusConflict || decode[errResp](t, rec).Code != "IDEMPOTENCY_KEY_REUSED" {
		t.Fatalf("other amount = %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.createAttempt("another-key", "O-1", "user-lan", 660000); rec.Code != http.StatusConflict || decode[errResp](t, rec).Code != "PAYMENT_IN_PROGRESS" {
		t.Fatalf("second active attempt = %d %s", rec.Code, rec.Body.String())
	}
}

func TestCreateAttemptValidatesInput(t *testing.T) {
	f := newFixture(t, true)
	for name, tc := range map[string]struct {
		headers map[string]string
		body    string
		code    string
	}{
		"missing key":     {map[string]string{internalauth.Header: internalToken}, `{"orderId":"O-1","userId":"u","amountVnd":1}`, "MISSING_IDEMPOTENCY_KEY"},
		"zero amount":     {internalHeaders("k"), `{"orderId":"O-1","userId":"u","amountVnd":0}`, "VALIDATION_FAILED"},
		"negative amount": {internalHeaders("k"), `{"orderId":"O-1","userId":"u","amountVnd":-5}`, "VALIDATION_FAILED"},
		"fractional":      {internalHeaders("k"), `{"orderId":"O-1","userId":"u","amountVnd":10.5}`, ""},
		"missing order":   {internalHeaders("k"), `{"userId":"u","amountVnd":1}`, "VALIDATION_FAILED"},
		"missing user":    {internalHeaders("k"), `{"orderId":"O-1","amountVnd":1}`, "VALIDATION_FAILED"},
		"unknown field":   {internalHeaders("k"), `{"orderId":"O-1","userId":"u","amountVnd":1,"status":"CAPTURED"}`, ""},
	} {
		rec := f.do(http.MethodPost, "/api/v1/payments/attempts", tc.headers, tc.body)
		if rec.Code != http.StatusBadRequest || (tc.code != "" && decode[errResp](t, rec).Code != tc.code) {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
}

func (f *fixture) simulate(user, attemptID, body string) *httptest.ResponseRecorder {
	return f.do(http.MethodPost, "/api/v1/payments/simulator/"+attemptID+"/outcome", map[string]string{"Authorization": "Bearer " + token(user)}, body)
}

func TestSimulatorPaymentPaths_SC01_SC04_SC05(t *testing.T) {
	f := newFixture(t, true)
	f.createAttempt("pay-O-1", "O-1", "user-lan", 660000)

	// SC05: unknown at 10:01 gives a window until 10:31.
	*f.clock = f.clock.Add(time.Minute)
	rec := f.simulate("user-lan", "PAY-O-1", `{"outcome":"UNKNOWN"}`)
	got := decode[attemptResp](t, rec)
	if rec.Code != http.StatusOK || got.Data.Status != "UNKNOWN" || got.Data.UnknownDeadline != "2026-11-15T10:31:00Z" {
		t.Fatalf("unknown = %d %s", rec.Code, rec.Body.String())
	}
	// 10:40: late capture of the full amount is owed back, never confirmed.
	*f.clock = f.clock.Add(39 * time.Minute)
	rec = f.simulate("user-lan", "PAY-O-1", `{"outcome":"CAPTURED"}`)
	got = decode[attemptResp](t, rec)
	if rec.Code != http.StatusOK || got.Data.Status != "CAPTURED_LATE" || got.Data.CapturedAmountVND != 660000 {
		t.Fatalf("late capture = %d %s", rec.Code, rec.Body.String())
	}
	sum, _ := f.store.Summary(context.Background(), "O-1")
	obs, _ := f.store.Obligations(context.Background(), "O-1")
	if sum.CapturedVND != 660000 || sum.RefundPending != 660000 || len(obs) != 1 || obs[0].AmountVND != 660000 {
		t.Fatalf("summary = %+v obligations = %+v", sum, obs)
	}
	// Repeating it changes nothing; a different final outcome is refused.
	if again := f.simulate("user-lan", "PAY-O-1", `{"outcome":"CAPTURED"}`); again.Code != http.StatusOK {
		t.Fatalf("replay = %d", again.Code)
	}
	if rec := f.simulate("user-lan", "PAY-O-1", `{"outcome":"FAILED"}`); rec.Code != http.StatusConflict || decode[errResp](t, rec).Code != "PAYMENT_ALREADY_FINAL" {
		t.Fatalf("flip = %d %s", rec.Code, rec.Body.String())
	}
	if obs, _ := f.store.Obligations(context.Background(), "O-1"); len(obs) != 1 {
		t.Fatalf("obligations after replays = %d, want 1", len(obs))
	}
}

func TestSimulatorSuccessAndFailure(t *testing.T) {
	f := newFixture(t, true)
	f.createAttempt("pay-O-1", "O-1", "user-lan", 660000)
	f.createAttempt("pay-O-2", "O-2", "user-lan", 100000)

	ok := decode[attemptResp](t, f.simulate("user-lan", "PAY-O-1", `{"outcome":"CAPTURED"}`))
	if ok.Data.Status != "CAPTURED" || ok.Data.CapturedAmountVND != 660000 {
		t.Fatalf("captured = %+v", ok.Data)
	}
	failed := decode[attemptResp](t, f.simulate("user-lan", "PAY-O-2", `{"outcome":"FAILED"}`))
	if failed.Data.Status != "FAILED" || failed.Data.CapturedAmountVND != 0 {
		t.Fatalf("failed = %+v", failed.Data)
	}
	// An explicit amount is recorded as reported: the order decides whether it matches (P04).
	f.createAttempt("pay-O-3", "O-3", "user-lan", 660000)
	short := decode[attemptResp](t, f.simulate("user-lan", "PAY-O-3", `{"outcome":"CAPTURED","amountVnd":650000}`))
	if short.Data.Status != "CAPTURED" || short.Data.CapturedAmountVND != 650000 {
		t.Fatalf("short capture = %+v", short.Data)
	}
}

func TestSimulatorIsOwnerOnlyValidatedAndSwitchable(t *testing.T) {
	f := newFixture(t, true)
	f.createAttempt("pay-O-1", "O-1", "user-lan", 660000)
	if rec := f.simulate("user-minh", "PAY-O-1", `{"outcome":"CAPTURED"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("other customer = %d", rec.Code)
	}
	if rec := f.simulate("user-lan", "PAY-missing", `{"outcome":"CAPTURED"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("missing = %d", rec.Code)
	}
	for _, body := range []string{`{"outcome":"MAYBE"}`, `{}`, `{"outcome":"CAPTURED","amountVnd":-1}`, `{"outcome":"CAPTURED","amountVnd":0}`, `{"outcome":"CAPTURED","extra":1}`} {
		if rec := f.simulate("user-lan", "PAY-O-1", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d %s", body, rec.Code, rec.Body.String())
		}
	}
	if rec := f.do(http.MethodPost, "/api/v1/payments/simulator/PAY-O-1/outcome", nil, `{"outcome":"CAPTURED"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous = %d", rec.Code)
	}
	off := newFixture(t, false)
	off.createAttempt("pay-O-1", "O-1", "user-lan", 660000)
	if rec := off.simulate("user-lan", "PAY-O-1", `{"outcome":"CAPTURED"}`); rec.Code != http.StatusNotFound || decode[errResp](t, rec).Code != "NOT_FOUND" {
		t.Fatalf("disabled simulator = %d %s", rec.Code, rec.Body.String())
	}
}

func TestGetAttemptIsOwnerOnly(t *testing.T) {
	f := newFixture(t, true)
	f.createAttempt("pay-O-1", "O-1", "user-lan", 660000)
	own := f.do(http.MethodGet, "/api/v1/payments/attempts/PAY-O-1", map[string]string{"Authorization": "Bearer " + token("user-lan")}, "")
	if own.Code != http.StatusOK || decode[attemptResp](t, own).Data.Status != "PROCESSING" {
		t.Fatalf("owner = %d %s", own.Code, own.Body.String())
	}
	if rec := f.do(http.MethodGet, "/api/v1/payments/attempts/PAY-O-1", map[string]string{"Authorization": "Bearer " + token("user-minh")}, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("other customer = %d", rec.Code)
	}
	if rec := f.do(http.MethodGet, "/api/v1/payments/attempts/PAY-O-1", nil, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous = %d", rec.Code)
	}
}

func TestWebhookChecksTheSignatureAndAppliesEachEventOnce(t *testing.T) {
	f := newFixture(t, false)
	f.createAttempt("pay-O-1", "O-1", "user-lan", 660000)
	body := `{"eventId":"evt-1","attemptId":"PAY-O-1","outcome":"CAPTURED","amountVnd":660000}`

	for name, sig := range map[string]string{"missing": "", "wrong": strings.Repeat("0", 64), "other body": sign(`{"x":1}`)} {
		rec := f.do(http.MethodPost, "/api/v1/payments/webhooks/simulator", map[string]string{"X-Signature": sig}, body)
		if rec.Code != http.StatusUnauthorized || decode[errResp](t, rec).Code != "INVALID_SIGNATURE" {
			t.Errorf("%s signature = %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if got, _ := f.store.Get(context.Background(), "PAY-O-1"); got.Status != "PROCESSING" {
		t.Fatalf("an unsigned webhook changed the attempt: %s", got.Status)
	}

	signed := map[string]string{"X-Signature": sign(body)}
	first := f.do(http.MethodPost, "/api/v1/payments/webhooks/simulator", signed, body)
	if first.Code != http.StatusOK || decode[attemptResp](t, first).Data.Status != "CAPTURED" {
		t.Fatalf("first delivery = %d %s", first.Code, first.Body.String())
	}
	replay := f.do(http.MethodPost, "/api/v1/payments/webhooks/simulator", signed, body)
	if replay.Code != http.StatusOK || !strings.Contains(replay.Body.String(), "already applied") {
		t.Fatalf("replay = %d %s", replay.Code, replay.Body.String())
	}
	if sum, _ := f.store.Summary(context.Background(), "O-1"); sum.CapturedVND != 660000 {
		t.Fatalf("captured = %d, want 660000 once", sum.CapturedVND)
	}
}

func TestWebhookRejectsBadPayloadsAndUnknownAttempts(t *testing.T) {
	f := newFixture(t, false)
	post := func(body string) *httptest.ResponseRecorder {
		return f.do(http.MethodPost, "/api/v1/payments/webhooks/simulator", map[string]string{"X-Signature": sign(body)}, body)
	}
	for _, body := range []string{
		`{"attemptId":"PAY-O-1","outcome":"CAPTURED","amountVnd":1}`,
		`{"eventId":"e","attemptId":"PAY-O-1","outcome":"MAYBE"}`,
		`{"eventId":"e","attemptId":"PAY-O-1","outcome":"CAPTURED","amountVnd":1,"extra":true}`,
		`nope`,
	} {
		if rec := post(body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d %s", body, rec.Code, rec.Body.String())
		}
	}
	if rec := post(`{"eventId":"e","attemptId":"PAY-none","outcome":"FAILED"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown attempt = %d", rec.Code)
	}
}

func TestWebhookWithoutAConfiguredSecretNeverValidates(t *testing.T) {
	authtest.Configure(t)
	srv := NewServer(domain.NewPaymentEngine(), attempt.NewMemoryStore(), Options{})
	body := `{"eventId":"e","attemptId":"PAY-O-1","outcome":"FAILED"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/payments/webhooks/simulator", strings.NewReader(body))
	req.Header.Set("X-Signature", sign(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestRefundReservationRouteIsInternalOnly(t *testing.T) {
	f := newFixture(t, false)
	body := `{"amountVnd":100}`
	if rec := f.do(http.MethodPost, "/api/v1/payments/O-001/refund-reservations", map[string]string{"Authorization": "Bearer " + token("user-lan")}, body); rec.Code != http.StatusUnauthorized {
		t.Fatalf("customer = %d, want 401", rec.Code)
	}
	if rec := f.do(http.MethodPost, "/api/v1/payments/O-001/refund-reservations", map[string]string{internalauth.Header: internalToken}, body); rec.Code != http.StatusCreated {
		t.Fatalf("internal = %d %s", rec.Code, rec.Body.String())
	}
}
