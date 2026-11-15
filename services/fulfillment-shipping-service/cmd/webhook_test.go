package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/services/fulfillment-shipping-service/internal/shipment"
)

const webhookSecret = "test-carrier-secret-0123456789abcdef"

var t0 = time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)

func webhookEnv(t *testing.T) *env {
	t.Helper()
	e := handoverEnv(t, &fakeStock{})
	e.srv = NewServer(Deps{Shipments: e.store, Stock: &fakeStock{}, WebhookSecret: webhookSecret, Timeline: e.tl, Now: func() time.Time { return t0 }})
	return e
}

func (e *env) carrier(t *testing.T, body, signature string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/shipments/webhooks/carrier", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if signature == "sign" {
		mac := hmac.New(sha256.New, []byte(webhookSecret))
		mac.Write([]byte(body))
		signature = hex.EncodeToString(mac.Sum(nil))
	}
	if signature != "" {
		req.Header.Set("X-Signature", signature)
	}
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	return rec
}

// dispatched packs and hands over O-1 so carrier notices have something to act on.
func dispatched(t *testing.T, e *env) {
	t.Helper()
	pack(t, e, "SHP-O-1", &hcmStaff)
	if rec := e.call(t, "POST", "/api/v1/admin/shipments/SHP-O-1/handover", `{}`, &hcmStaff); rec.Code != 200 {
		t.Fatalf("handover: %d %s", rec.Code, rec.Body)
	}
}

func TestTheWebhookNeedsAValidSignature(t *testing.T) {
	e := webhookEnv(t)
	dispatched(t, e)
	body := `{"eventId":"c-1","shipmentId":"SHP-O-1","type":"IN_TRANSIT"}`
	for name, sig := range map[string]string{"missing": "", "wrong": strings.Repeat("0", 64), "garbage": "zz"} {
		if rec := e.carrier(t, body, sig); rec.Code != 401 || errCode(t, rec) != "INVALID_SIGNATURE" {
			t.Errorf("%s: %d %s", name, rec.Code, errCode(t, rec))
		}
	}
	if rec := e.carrier(t, body, "sign"); rec.Code != 200 {
		t.Fatalf("signed: %d %s", rec.Code, rec.Body)
	}
	// A body changed after signing no longer matches.
	mac := hmac.New(sha256.New, []byte(webhookSecret))
	mac.Write([]byte(body))
	if rec := e.carrier(t, strings.Replace(body, "c-1", "c-2", 1), hex.EncodeToString(mac.Sum(nil))); rec.Code != 401 {
		t.Fatalf("tampered: %d", rec.Code)
	}
}

func TestADeliveredNoticeCompletesTheParcelOnceAndStartsTheReturnWindow(t *testing.T) {
	e := webhookEnv(t)
	dispatched(t, e)
	e.carrier(t, `{"eventId":"c-1","shipmentId":"SHP-O-1","type":"IN_TRANSIT"}`, "sign")
	body := `{"eventId":"c-2","shipmentId":"SHP-O-1","type":"DELIVERED"}`
	for i := 0; i < 5; i++ {
		rec := e.carrier(t, body, "sign")
		if rec.Code != 200 {
			t.Fatalf("notice %d: %d %s", i, rec.Code, rec.Body)
		}
		if got := decode[noticeResult](t, rec); got.Status != "DELIVERED" || (i == 0) != got.Applied {
			t.Fatalf("notice %d result = %+v", i, got)
		}
	}
	s, _ := e.store.Get(context.Background(), "SHP-O-1")
	if s.ReturnWindowEndsAt == nil || !s.ReturnWindowEndsAt.Equal(t0.Add(7*24*time.Hour)) || s.Items[0].DeliveredQuantity != 2 {
		t.Fatalf("shipment = %+v", s)
	}
	delivered := 0
	for _, typ := range e.store.EventTypes("SHP-O-1") {
		if typ == "fulfillment.shipment_delivered.v1" {
			delivered++
		}
	}
	if delivered != 1 {
		t.Fatalf("delivered events = %d, want 1", delivered)
	}
}

func TestTwentyConcurrentCopiesOfOneNoticeApplyOnce(t *testing.T) {
	e := webhookEnv(t)
	dispatched(t, e)
	var wg sync.WaitGroup
	var mu sync.Mutex
	applied := 0
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := e.carrier(t, `{"eventId":"c-9","shipmentId":"SHP-O-1","type":"DELIVERED"}`, "sign")
			if rec.Code != 200 {
				t.Errorf("status %d %s", rec.Code, rec.Body)
				return
			}
			if decode[noticeResult](t, rec).Applied {
				mu.Lock()
				applied++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if applied != 1 {
		t.Fatalf("applied %d times", applied)
	}
}

func TestADuplicateEventIdIsAcknowledgedWithoutChange(t *testing.T) {
	e := webhookEnv(t)
	dispatched(t, e)
	e.carrier(t, `{"eventId":"c-1","shipmentId":"SHP-O-1","type":"IN_TRANSIT"}`, "sign")
	s1, _ := e.store.Get(context.Background(), "SHP-O-1")
	// Same id, different type: still a duplicate, nothing changes.
	rec := e.carrier(t, `{"eventId":"c-1","shipmentId":"SHP-O-1","type":"DELIVERED"}`, "sign")
	got := decode[noticeResult](t, rec)
	s2, _ := e.store.Get(context.Background(), "SHP-O-1")
	if rec.Code != 200 || !got.Duplicate || got.Applied || s2.Version != s1.Version || s2.Status != shipment.StatusInTransit {
		t.Fatalf("duplicate: %d %+v, shipment %s v%d", rec.Code, got, s2.Status, s2.Version)
	}
}

func TestARepeatedNoticeForAStateAlreadyReachedIsADuplicate(t *testing.T) {
	e := webhookEnv(t)
	dispatched(t, e)
	e.carrier(t, `{"eventId":"c-1","shipmentId":"SHP-O-1","type":"IN_TRANSIT"}`, "sign")
	e.carrier(t, `{"eventId":"c-2","shipmentId":"SHP-O-1","type":"DELIVERED"}`, "sign")
	s1, _ := e.store.Get(context.Background(), "SHP-O-1")
	// The same event again, and the same state under another event id: both change nothing and say so.
	for _, body := range []string{
		`{"eventId":"c-2","shipmentId":"SHP-O-1","type":"DELIVERED"}`,
		`{"eventId":"c-3","shipmentId":"SHP-O-1","type":"DELIVERED"}`,
	} {
		rec := e.carrier(t, body, "sign")
		got := decode[noticeResult](t, rec)
		s2, _ := e.store.Get(context.Background(), "SHP-O-1")
		if rec.Code != 200 || !got.Duplicate || got.Applied || s2.Version != s1.Version {
			t.Fatalf("%s: %d %+v, version %d (was %d)", body, rec.Code, got, s2.Version, s1.Version)
		}
	}
}

func TestAFailedDeliveryKeepsTheReason(t *testing.T) {
	e := webhookEnv(t)
	dispatched(t, e)
	rec := e.carrier(t, `{"eventId":"c-1","shipmentId":"SHP-O-1","type":"DELIVERY_FAILED","reason":"customer not reachable"}`, "sign")
	s, _ := e.store.Get(context.Background(), "SHP-O-1")
	if rec.Code != 200 || s.Status != shipment.StatusDeliveryFailed || s.FailureReason != "customer not reachable" || s.DeliveredAt != nil {
		t.Fatalf("failed: %d %+v", rec.Code, s)
	}
	if rec := e.carrier(t, `{"eventId":"c-2","shipmentId":"SHP-O-1","type":"DELIVERY_FAILED"}`, "sign"); rec.Code != 400 {
		t.Fatalf("reasonless failure: %d", rec.Code)
	}
}

func TestNoticesForUnknownOrUndispatchedParcelsAreRefused(t *testing.T) {
	e := webhookEnv(t)
	for name, tc := range map[string]struct {
		body   string
		status int
		code   string
	}{
		"not handed over": {`{"eventId":"c-1","shipmentId":"SHP-O-2","type":"DELIVERED"}`, 409, "SHIPMENT_NOT_DISPATCHED"},
		"unknown":         {`{"eventId":"c-2","shipmentId":"SHP-NOPE","type":"DELIVERED"}`, 404, "SHIPMENT_NOT_FOUND"},
		"bad type":        {`{"eventId":"c-3","shipmentId":"SHP-O-2","type":"EXPLODED"}`, 400, "VALIDATION_FAILED"},
		"no event id":     {`{"shipmentId":"SHP-O-2","type":"DELIVERED"}`, 400, "VALIDATION_FAILED"},
		"unknown field":   {`{"eventId":"c-4","shipmentId":"SHP-O-2","type":"DELIVERED","x":1}`, 400, "VALIDATION_FAILED"},
	} {
		rec := e.carrier(t, tc.body, "sign")
		if rec.Code != tc.status || errCode(t, rec) != tc.code {
			t.Errorf("%s: %d %s", name, rec.Code, errCode(t, rec))
		}
	}
	if s, _ := e.store.Get(context.Background(), "SHP-O-2"); s.Status != shipment.StatusPendingPick {
		t.Fatalf("status = %s", s.Status)
	}
}

func TestWithoutAConfiguredSecretNothingValidates(t *testing.T) {
	e := newEnv(t) // NewServer without a webhook secret
	body := `{"eventId":"c-1","shipmentId":"SHP-O-1","type":"IN_TRANSIT"}`
	if rec := e.carrier(t, body, "sign"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d", rec.Code)
	}
}
