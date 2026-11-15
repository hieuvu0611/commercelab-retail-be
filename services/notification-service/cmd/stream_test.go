package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/hieuvu0611/commercelab-retail-be/contracts/events"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth/authtest"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/middleware"
	"github.com/hieuvu0611/commercelab-retail-be/services/notification-service/internal/stopalert"
)

var (
	hcmStaff   = auth.Identity{UserID: "staff-hcm", Roles: []string{"WAREHOUSE_STAFF"}, WarehouseScopes: []string{"WH-HCM-01"}}
	hnStaff    = auth.Identity{UserID: "staff-hn", Roles: []string{"WAREHOUSE_STAFF"}, WarehouseScopes: []string{"WH-HN-01"}}
	allManager = auth.Identity{UserID: "mgr-all", Roles: []string{"WAREHOUSE_MGR"}, WarehouseScopes: []string{"ALL"}}
	customer   = auth.Identity{UserID: "user-lan", Roles: []string{"CUSTOMER"}}
)

type streamEnv struct {
	srv *httptest.Server
	hub *stopalert.Hub
}

func newStreamEnv(t *testing.T, keepAlive time.Duration) *streamEnv {
	t.Helper()
	authtest.Configure(t)
	hub := stopalert.NewHub()
	mux := http.NewServeMux()
	registerStopAlertRoutes(mux, hub, keepAlive)
	srv := httptest.NewServer(middleware.Chain(mux))
	t.Cleanup(srv.Close)
	return &streamEnv{srv: srv, hub: hub}
}

func (e *streamEnv) open(t *testing.T, ctx context.Context, warehouse string, who *auth.Identity) *http.Response {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, e.srv.URL+"/api/v1/notifications/warehouse/"+warehouse+"/stream", nil)
	if who != nil {
		tok, err := auth.SignAccessToken(*who)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })
	return res
}

type sse struct{ event, id, data string }

// read returns the next event (comments are returned with event ":").
func readEvent(t *testing.T, r *bufio.Reader) sse {
	t.Helper()
	var ev sse
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				ev.event = "EOF"
				return
			}
			line = strings.TrimRight(line, "\n")
			switch {
			case line == "":
				if ev.event != "" || ev.data != "" {
					return
				}
			case strings.HasPrefix(line, ":"):
				ev.event = ":"
				return
			case strings.HasPrefix(line, "event: "):
				ev.event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "id: "):
				ev.id = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "data: "):
				ev.data = strings.TrimPrefix(line, "data: ")
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("no event within 2s")
	}
	return ev
}

func stopEvent(id, shipment, order, warehouse string) events.DomainEvent {
	return events.DomainEvent{EventID: id, EventType: events.EventShipmentStopRequested, AggregateType: "Shipment", AggregateID: shipment,
		Payload: map[string]any{"shipmentId": shipment, "orderId": order, "warehouseId": warehouse, "requestedAt": "2026-11-15T10:00:00Z"}}
}

func TestStreamNeedsASignedInWarehouseUserOfThatWarehouse(t *testing.T) {
	e := newStreamEnv(t, time.Minute)
	for name, tc := range map[string]struct {
		who    *auth.Identity
		status int
		code   string
	}{
		"anonymous":       {nil, 401, "UNAUTHORIZED"},
		"customer":        {&customer, 403, "FORBIDDEN"},
		"other warehouse": {&hnStaff, 403, "FORBIDDEN_WAREHOUSE_SCOPE"},
	} {
		res := e.open(t, context.Background(), "WH-HCM-01", tc.who)
		var body struct {
			Code string `json:"code"`
		}
		_ = json.NewDecoder(res.Body).Decode(&body)
		if res.StatusCode != tc.status || body.Code != tc.code {
			t.Errorf("%s: %d %q, want %d %q", name, res.StatusCode, body.Code, tc.status, tc.code)
		}
	}
	if n := e.hub.Subscribers("WH-HCM-01"); n != 0 {
		t.Fatalf("a refused request left %d subscribers", n)
	}
	if res := e.open(t, context.Background(), "bad id!", &allManager); res.StatusCode != 400 {
		t.Fatalf("bad warehouse id = %d", res.StatusCode)
	}
}

func TestStopAlertReachesTheWarehouseScreenAndNobodyElse(t *testing.T) {
	e := newStreamEnv(t, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hcm := e.open(t, ctx, "WH-HCM-01", &hcmStaff)
	hn := e.open(t, ctx, "WH-HN-01", &hnStaff)
	all := e.open(t, ctx, "WH-HCM-01", &allManager) // a manager of every warehouse sees the same alerts
	if hcm.StatusCode != 200 || !strings.HasPrefix(hcm.Header.Get("Content-Type"), "text/event-stream") || hcm.Header.Get("Cache-Control") != "no-cache" {
		t.Fatalf("stream = %d %v", hcm.StatusCode, hcm.Header)
	}
	hcmR, hnR, allR := bufio.NewReader(hcm.Body), bufio.NewReader(hn.Body), bufio.NewReader(all.Body)
	for _, r := range []*bufio.Reader{hcmR, hnR, allR} {
		if ev := readEvent(t, r); ev.event != "connected" {
			t.Fatalf("first event = %+v", ev)
		}
	}
	_ = e.hub.HandleEvent(context.Background(), stopEvent("evt-1", "SHP-O-1", "O-1", "WH-HCM-01"))
	for name, r := range map[string]*bufio.Reader{"staff": hcmR, "manager": allR} {
		ev := readEvent(t, r)
		var alert map[string]any
		if err := json.Unmarshal([]byte(ev.data), &alert); err != nil {
			t.Fatalf("%s: %q %v", name, ev.data, err)
		}
		if ev.event != "stop_requested" || ev.id != "evt-1" || alert["type"] != "WAREHOUSE_STOP_SIGNAL" || alert["shipmentId"] != "SHP-O-1" ||
			alert["orderId"] != "O-1" || alert["warehouseId"] != "WH-HCM-01" || alert["requestedAt"] != "2026-11-15T10:00:00Z" || alert["message"] == "" {
			t.Fatalf("%s: event = %+v data = %v", name, ev, alert)
		}
	}
	// The other warehouse's screen stays quiet: the next thing it sees is its own alert.
	_ = e.hub.HandleEvent(context.Background(), stopEvent("evt-2", "SHP-O-9", "O-9", "WH-HN-01"))
	if ev := readEvent(t, hnR); ev.event != "stop_requested" || !strings.Contains(ev.data, "SHP-O-9") || strings.Contains(ev.data, "SHP-O-1") {
		t.Fatalf("hn event = %+v", ev)
	}
	// Resolution closes the alert.
	_ = e.hub.HandleEvent(context.Background(), events.DomainEvent{EventID: "evt-3", EventType: events.EventShipmentHandedOver, Payload: map[string]any{"shipmentId": "SHP-O-1", "orderId": "O-1", "warehouseId": "WH-HCM-01"}})
	if ev := readEvent(t, hcmR); ev.event != "stop_resolved" || !strings.Contains(ev.data, `"outcome":"HANDED_OVER"`) {
		t.Fatalf("resolution = %+v", ev)
	}
}

func TestALateScreenGetsTheOpenAlertAndAKeepAlive(t *testing.T) {
	e := newStreamEnv(t, 40*time.Millisecond)
	_ = e.hub.HandleEvent(context.Background(), stopEvent("evt-1", "SHP-O-1", "O-1", "WH-HCM-01"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := bufio.NewReader(e.open(t, ctx, "WH-HCM-01", &hcmStaff).Body)
	if ev := readEvent(t, r); ev.event != "connected" {
		t.Fatalf("first = %+v", ev)
	}
	if ev := readEvent(t, r); ev.event != "stop_requested" || !strings.Contains(ev.data, "SHP-O-1") {
		t.Fatalf("open alert = %+v", ev)
	}
	if ev := readEvent(t, r); ev.event != ":" {
		t.Fatalf("keep-alive = %+v", ev)
	}
}

func TestClosingTheScreenReleasesTheSubscription(t *testing.T) {
	e := newStreamEnv(t, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	r := bufio.NewReader(e.open(t, ctx, "WH-HCM-01", &hcmStaff).Body)
	readEvent(t, r)
	if n := e.hub.Subscribers("WH-HCM-01"); n != 1 {
		t.Fatalf("subscribers = %d", n)
	}
	cancel()
	deadline := time.Now().Add(2 * time.Second)
	for e.hub.Subscribers("WH-HCM-01") != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := e.hub.Subscribers("WH-HCM-01"); n != 0 {
		t.Fatalf("subscribers after the client left = %d", n)
	}
}

func TestAnExpiringTokenEndsTheStreamSoTheScreenReconnectsWithAFreshOne(t *testing.T) {
	now := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	exp := jwtTime(now.Add(90 * time.Second))
	if d := untilExpiry(&auth.Claims{RegisteredClaims: registered(exp)}, now); d != 90*time.Second {
		t.Fatalf("until expiry = %v", d)
	}
	if d := untilExpiry(&auth.Claims{}, now); d != maxStreamLifetime {
		t.Fatalf("a token without expiry = %v, want the maximum lifetime", d)
	}
	if d := untilExpiry(&auth.Claims{RegisteredClaims: registered(jwtTime(now.Add(-time.Second)))}, now); d != 0 {
		t.Fatalf("an expired token = %v, want 0", d)
	}
	if d := untilExpiry(&auth.Claims{RegisteredClaims: registered(jwtTime(now.Add(48 * time.Hour)))}, now); d != maxStreamLifetime {
		t.Fatalf("a far expiry = %v, want it capped", d)
	}
}

func jwtTime(t time.Time) *jwt.NumericDate { return jwt.NewNumericDate(t) }

func registered(exp *jwt.NumericDate) jwt.RegisteredClaims {
	return jwt.RegisteredClaims{ExpiresAt: exp}
}
