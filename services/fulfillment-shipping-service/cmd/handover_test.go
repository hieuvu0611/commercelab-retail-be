package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/services/fulfillment-shipping-service/internal/inventory"
	"github.com/hieuvu0611/commercelab-retail-be/services/fulfillment-shipping-service/internal/shipment"
)

type fakeStock struct {
	calls []string // "orderId@warehouseId"
	err   error
}

func (f *fakeStock) Handover(_ context.Context, bearer, orderID, warehouseID string) error {
	f.calls = append(f.calls, orderID+"@"+warehouseID)
	if bearer == "" {
		return errors.New("no bearer forwarded")
	}
	return f.err
}

func handoverEnv(t *testing.T, stock *fakeStock) *env {
	t.Helper()
	e := newEnv(t)
	e.srv = NewServer(Deps{Shipments: e.store, Stock: stock, Timeline: e.tl, Now: func() time.Time { return time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC) }})
	return e
}

func pack(t *testing.T, e *env, id string, who *auth.Identity) {
	t.Helper()
	for _, body := range []string{`{"skuId":"SKU-A","quantity":2}`, `{"skuId":"SKU-B","quantity":1}`} {
		if rec := e.call(t, "POST", "/api/v1/admin/shipments/"+id+"/scan", body, who); rec.Code != 200 {
			t.Fatalf("scan: %d %s", rec.Code, rec.Body)
		}
	}
	if rec := e.call(t, "POST", "/api/v1/admin/shipments/"+id+"/pack", `{}`, who); rec.Code != 200 {
		t.Fatalf("pack: %d %s", rec.Code, rec.Body)
	}
}

func TestHandoverTakesTheWarehouseFromTheShipmentAndRunsOnce(t *testing.T) {
	stock := &fakeStock{}
	e := handoverEnv(t, stock)
	pack(t, e, "SHP-O-2", &hnStaff) // O-2 lives in WH-HN-01
	for i := 0; i < 3; i++ {
		rec := e.call(t, "POST", "/api/v1/admin/shipments/SHP-O-2/handover", `{}`, &hnStaff)
		v := decode[shipmentView](t, rec)
		if rec.Code != 200 || v.Status != "HANDED_OVER" || v.HandedOverAt == nil {
			t.Fatalf("handover %d: %d %s", i, rec.Code, rec.Body)
		}
	}
	if len(stock.calls) != 1 || stock.calls[0] != "O-2@WH-HN-01" {
		t.Fatalf("inventory calls = %v, want one for O-2 in WH-HN-01", stock.calls)
	}
	s, _ := e.store.Get(context.Background(), "SHP-O-2")
	if got := e.store.EventTypes("SHP-O-2"); len(got) != 1 || got[0] != "fulfillment.shipment_handed_over.v1" || s.Version != 5 {
		t.Fatalf("events %v, version %d", got, s.Version)
	}
}

func TestHandoverIsRefusedUntilPackedAndOutsideTheScope(t *testing.T) {
	stock := &fakeStock{}
	e := handoverEnv(t, stock)
	if rec := e.call(t, "POST", "/api/v1/admin/shipments/SHP-O-1/handover", `{}`, &hcmStaff); rec.Code != 409 || errCode(t, rec) != "HANDOVER_NOT_ALLOWED" {
		t.Fatalf("unpacked: %d %s", rec.Code, errCode(t, rec))
	}
	if rec := e.call(t, "POST", "/api/v1/admin/shipments/SHP-O-2/handover", `{}`, &hcmStaff); rec.Code != 403 || errCode(t, rec) != "FORBIDDEN_WAREHOUSE_SCOPE" {
		t.Fatalf("other warehouse: %d %s", rec.Code, errCode(t, rec))
	}
	if rec := e.call(t, "POST", "/api/v1/admin/shipments/SHP-O-1/handover", `{}`, &customerOne); rec.Code != 403 {
		t.Fatalf("customer: %d", rec.Code)
	}
	if len(stock.calls) != 0 {
		t.Fatalf("inventory was called: %v", stock.calls)
	}
}

func TestAStaleVersionStopsHandoverBeforeInventoryIsTouched(t *testing.T) {
	stock := &fakeStock{}
	e := handoverEnv(t, stock)
	pack(t, e, "SHP-O-1", &hcmStaff)
	if rec := e.call(t, "POST", "/api/v1/admin/shipments/SHP-O-1/handover", `{"version":1}`, &hcmStaff); rec.Code != 409 || errCode(t, rec) != "STALE_VERSION" {
		t.Fatalf("stale: %d %s", rec.Code, errCode(t, rec))
	}
	if len(stock.calls) != 0 {
		t.Fatal("inventory was called")
	}
}

func TestAnInventoryFailureLeavesTheParcelPackedAndMapsTheError(t *testing.T) {
	for name, tc := range map[string]struct {
		err    error
		status int
		code   string
	}{
		"rejected":    {inventory.ErrRejected, 409, "INVENTORY_HANDOVER_REJECTED"},
		"scope":       {inventory.ErrForbidden, 403, "FORBIDDEN_WAREHOUSE_SCOPE"},
		"unavailable": {dependency.Wrap(errors.New("down")), 503, "DEPENDENCY_UNAVAILABLE"},
	} {
		stock := &fakeStock{err: tc.err}
		e := handoverEnv(t, stock)
		pack(t, e, "SHP-O-1", &hcmStaff)
		rec := e.call(t, "POST", "/api/v1/admin/shipments/SHP-O-1/handover", `{}`, &hcmStaff)
		if rec.Code != tc.status || errCode(t, rec) != tc.code {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
		if s, _ := e.store.Get(context.Background(), "SHP-O-1"); s.Status != shipment.StatusPacked {
			t.Errorf("%s: status %s, want PACKED", name, s.Status)
		}
	}
}
