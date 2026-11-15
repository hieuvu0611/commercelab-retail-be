package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/contracts/events"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/services/fulfillment-shipping-service/internal/inventory"
	"github.com/hieuvu0611/commercelab-retail-be/services/fulfillment-shipping-service/internal/shipment"
)

// arbiterStock behaves like inventory's reservation row: the handover and the release exclude each other, the
// first one wins and repeating the winner changes nothing. It counts what really took effect.
type arbiterStock struct {
	mu                    sync.Mutex
	state                 string // "", "HANDED_OVER" or "RELEASED"
	handoverFx, releaseFx int
}

func (a *arbiterStock) Handover(context.Context, string, string, string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch a.state {
	case "RELEASED":
		return inventory.ErrRejected
	case "":
		a.state = "HANDED_OVER"
		a.handoverFx++
	}
	return nil
}

func (a *arbiterStock) ReleaseCommitted(context.Context, string, string, string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch a.state {
	case "HANDED_OVER":
		return inventory.ErrStockLeft
	case "":
		a.state = "RELEASED"
		a.releaseFx++
	}
	return nil
}

func stopEnv(t *testing.T, stock StockHandover) *env {
	t.Helper()
	e := newEnv(t)
	e.srv = NewServer(Deps{Shipments: e.store, Stock: stock, Timeline: e.tl, Now: func() time.Time { return time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC) }})
	return e
}

func TestConfirmStopIsForWarehouseStaffOfTheShipmentsWarehouse(t *testing.T) {
	e := stopEnv(t, &fakeStock{})
	pack(t, e, "SHP-O-1", &hcmStaff)
	e.stop(t, "O-1", stopBody, true)
	const path = "/api/v1/admin/shipments/SHP-O-1/confirm-stop"
	for name, tc := range map[string]struct {
		who  *auth.Identity
		want int
	}{"anonymous": {nil, 401}, "customer": {&customerOne, 403}, "other warehouse": {&hnStaff, 403}} {
		if rec := e.call(t, "POST", path, `{}`, tc.who); rec.Code != tc.want {
			t.Errorf("%s = %d %s", name, rec.Code, rec.Body)
		}
	}
	if s, _ := e.store.Get(context.Background(), "SHP-O-1"); s.Status != shipment.StatusPacked {
		t.Fatalf("a refused confirmation changed the parcel: %s", s.Status)
	}
}

func TestConfirmStopNeedsAStopRequest(t *testing.T) {
	stock := &fakeStock{}
	e := stopEnv(t, stock)
	pack(t, e, "SHP-O-1", &hcmStaff)
	rec := e.call(t, "POST", "/api/v1/admin/shipments/SHP-O-1/confirm-stop", `{}`, &hcmStaff)
	if rec.Code != 409 || errCode(t, rec) != "NO_STOP_REQUEST" || len(stock.released) != 0 {
		t.Fatalf("%d %s released=%v", rec.Code, rec.Body, stock.released)
	}
}

func TestStopRequestThenConfirmStopWinsSC06A(t *testing.T) {
	stock := &fakeStock{}
	e := stopEnv(t, stock)
	pack(t, e, "SHP-O-1", &hcmStaff)
	for i := 0; i < 2; i++ { // a repeated request is one request
		if got := decode[stopResp](t, e.stop(t, "O-1", stopBody, true)); got.Result != "STOP_REQUESTED" || got.ShipmentStatus != "PACKED" {
			t.Fatalf("request #%d = %+v", i, got)
		}
	}
	types := e.store.EventTypes("SHP-O-1")
	if countOf(types, events.EventShipmentStopRequested) != 1 {
		t.Fatalf("events = %v, want one stop request alert", types)
	}
	view := decode[shipmentView](t, e.call(t, "GET", "/api/v1/admin/shipments/SHP-O-1", "", &hcmStaff))
	if view.StopRequestedAt == nil || view.Status != "PACKED" {
		t.Fatalf("view = %+v, want the pending request visible", view)
	}
	for i := 0; i < 2; i++ {
		rec := e.call(t, "POST", "/api/v1/admin/shipments/SHP-O-1/confirm-stop", `{}`, &hcmStaff)
		if v := decode[shipmentView](t, rec); rec.Code != 200 || v.Status != "STOPPED_FOR_CANCEL" {
			t.Fatalf("confirm #%d = %d %s", i, rec.Code, rec.Body)
		}
	}
	if len(stock.released) != 1 || stock.released[0] != "O-1@WH-HCM-01" {
		t.Fatalf("inventory releases = %v, want exactly one for the shipment's warehouse", stock.released)
	}
	if rec := e.call(t, "POST", "/api/v1/admin/shipments/SHP-O-1/handover", `{}`, &hcmStaff); rec.Code != 409 {
		t.Fatalf("handover after the stop = %d %s", rec.Code, rec.Body)
	}
	if len(stock.calls) != 0 {
		t.Fatalf("the stock was handed over for a stopped parcel: %v", stock.calls)
	}
	types = e.store.EventTypes("SHP-O-1")
	if countOf(types, events.EventShipmentStopped) != 1 || countOf(types, events.EventShipmentHandedOver) != 0 {
		t.Fatalf("events = %v", types)
	}
}

func TestStopRequestThenHandoverWinsSC06B(t *testing.T) {
	stock := &fakeStock{}
	e := stopEnv(t, stock)
	pack(t, e, "SHP-O-1", &hcmStaff)
	e.stop(t, "O-1", stopBody, true)
	if rec := e.call(t, "POST", "/api/v1/admin/shipments/SHP-O-1/handover", `{}`, &hcmStaff); rec.Code != 200 {
		t.Fatalf("handover = %d %s", rec.Code, rec.Body)
	}
	rec := e.call(t, "POST", "/api/v1/admin/shipments/SHP-O-1/confirm-stop", `{}`, &hcmStaff)
	if rec.Code != 409 || errCode(t, rec) != "HANDOVER_ALREADY_DONE" || len(stock.released) != 0 {
		t.Fatalf("confirm after handover = %d %s released=%v", rec.Code, rec.Body, stock.released)
	}
	if got := decode[stopResp](t, e.stop(t, "O-1", stopBody, true)); got.Result != "TOO_LATE" {
		t.Fatalf("stop after handover = %+v", got)
	}
	if s, _ := e.store.Get(context.Background(), "SHP-O-1"); s.Status != shipment.StatusHandedOver {
		t.Fatalf("status = %s", s.Status)
	}
}

func TestConfirmStopStockAnswers(t *testing.T) {
	for name, tc := range map[string]struct {
		err    error
		status int
		code   string
	}{
		"handover won at inventory": {inventory.ErrStockLeft, 409, "HANDOVER_ALREADY_DONE"},
		"scope":                     {inventory.ErrForbidden, 403, "FORBIDDEN_WAREHOUSE_SCOPE"},
		"rejected":                  {inventory.ErrRejected, 409, "INVENTORY_RELEASE_REJECTED"},
		"unavailable":               {dependency.Wrap(errors.New("down")), 503, "DEPENDENCY_UNAVAILABLE"},
	} {
		stock := &fakeStock{releaseErr: tc.err}
		e := stopEnv(t, stock)
		pack(t, e, "SHP-O-1", &hcmStaff)
		e.stop(t, "O-1", stopBody, true)
		rec := e.call(t, "POST", "/api/v1/admin/shipments/SHP-O-1/confirm-stop", `{}`, &hcmStaff)
		if rec.Code != tc.status || errCode(t, rec) != tc.code {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
		if s, _ := e.store.Get(context.Background(), "SHP-O-1"); s.Status != shipment.StatusPacked {
			t.Errorf("%s: status %s, want PACKED (nothing stored without the release)", name, s.Status)
		}
	}
}

func TestConfirmStopWithAStaleVersionIsRefused(t *testing.T) {
	stock := &fakeStock{}
	e := stopEnv(t, stock)
	pack(t, e, "SHP-O-1", &hcmStaff)
	e.stop(t, "O-1", stopBody, true)
	rec := e.call(t, "POST", "/api/v1/admin/shipments/SHP-O-1/confirm-stop", `{"version":1}`, &hcmStaff)
	if rec.Code != 409 || errCode(t, rec) != "STALE_VERSION" || len(stock.released) != 0 {
		t.Fatalf("%d %s released=%v", rec.Code, rec.Body, stock.released)
	}
}

// The staff confirm-stop and the handover race for the same parcel. Whoever inventory's reservation lets in first
// wins; the other gets a 409 and nothing of it takes effect, so the parcel is never both handed over and stopped.
func TestConfirmStopAndHandoverRaceHasExactlyOneWinnerSC06(t *testing.T) {
	for round := 0; round < 40; round++ {
		stock := &arbiterStock{}
		e := stopEnv(t, stock)
		pack(t, e, "SHP-O-1", &hcmStaff)
		e.stop(t, "O-1", stopBody, true)
		var handover, confirm int
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			handover = e.call(t, "POST", "/api/v1/admin/shipments/SHP-O-1/handover", `{}`, &hcmStaff).Code
		}()
		go func() {
			defer wg.Done()
			confirm = e.call(t, "POST", "/api/v1/admin/shipments/SHP-O-1/confirm-stop", `{}`, &hcmStaff).Code
		}()
		wg.Wait()
		s, _ := e.store.Get(context.Background(), "SHP-O-1")
		types := e.store.EventTypes("SHP-O-1")
		handedOver, stopped := s.Status == shipment.StatusHandedOver, s.Status == shipment.StatusStoppedForCancel
		if handedOver == stopped {
			t.Fatalf("round %d: status %s after handover=%d confirm=%d, want exactly one winner", round, s.Status, handover, confirm)
		}
		if handedOver && (handover != 200 || confirm != 409 || stock.handoverFx != 1 || stock.releaseFx != 0 || countOf(types, events.EventShipmentStopped) != 0) {
			t.Fatalf("round %d: handover won but handover=%d confirm=%d fx=%d/%d events=%v", round, handover, confirm, stock.handoverFx, stock.releaseFx, types)
		}
		if stopped && (confirm != 200 || handover != 409 || stock.releaseFx != 1 || stock.handoverFx != 0 || countOf(types, events.EventShipmentHandedOver) != 0) {
			t.Fatalf("round %d: stop won but handover=%d confirm=%d fx=%d/%d events=%v", round, handover, confirm, stock.handoverFx, stock.releaseFx, types)
		}
	}
}
