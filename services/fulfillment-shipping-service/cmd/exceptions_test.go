package main

import (
	"context"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/services/fulfillment-shipping-service/internal/shipment"
)

var coordinator = auth.Identity{UserID: "coord-1", Roles: []string{"DELIVERY_COORD"}}

type caseRow struct {
	CaseID     string `json:"caseId"`
	Type       string `json:"type"`
	Status     string `json:"status"`
	ShipmentID string `json:"shipmentId"`
	SkuID      string `json:"skuId"`
	Quantity   int64  `json:"quantity"`
	SkuBlocked bool   `json:"skuBlocked"`
	Reason     string `json:"reason"`
}

func TestAPickShortageBlocksTheSkuOpensACaseAndNeverReportsDispatch(t *testing.T) {
	e := webhookEnv(t)
	e.call(t, "POST", "/api/v1/admin/shipments/SHP-O-1/scan", `{"skuId":"SKU-A","quantity":1}`, &hcmStaff)
	rec := e.call(t, "POST", "/api/v1/admin/shipments/SHP-O-1/report-shortage", `{"skuId":"SKU-A","quantity":1,"reason":"shelf empty"}`, &hcmStaff)
	if v := decode[shipmentView](t, rec); rec.Code != 200 || v.Status != "PICK_BLOCKED" {
		t.Fatalf("shortage: %d %s", rec.Code, rec.Body)
	}
	for _, path := range []string{"/pack", "/handover", "/scan"} {
		body := `{}`
		if path == "/scan" {
			body = `{"skuId":"SKU-A","quantity":1}`
		}
		rec := e.call(t, "POST", "/api/v1/admin/shipments/SHP-O-1"+path, body, &hcmStaff)
		if rec.Code != 409 {
			t.Errorf("%s after shortage: %d %s", path, rec.Code, rec.Body)
		}
	}
	if rec := e.carrier(t, `{"eventId":"c-1","shipmentId":"SHP-O-1","type":"DELIVERED"}`, "sign"); rec.Code != 409 {
		t.Fatalf("carrier notice for a blocked parcel: %d", rec.Code)
	}
	for _, typ := range e.store.EventTypes("SHP-O-1") {
		if typ == "fulfillment.shipment_handed_over.v1" || typ == "fulfillment.shipment_delivered.v1" {
			t.Fatalf("dispatch was reported: %s", typ)
		}
	}
	cases := decode[[]caseRow](t, e.call(t, "GET", "/api/v1/admin/fulfillment-cases?status=OPEN", "", &hcmStaff))
	if len(cases) != 1 || cases[0].Type != "PICK_SHORTAGE" || !cases[0].SkuBlocked || cases[0].SkuID != "SKU-A" || cases[0].Quantity != 1 || cases[0].Reason != "shelf empty" {
		t.Fatalf("cases = %+v", cases)
	}
	if got := e.store.EventTypes("SHP-O-1"); got[len(got)-1] != "support.exception_case_opened.v1" {
		t.Fatalf("events = %v", got)
	}
}

func TestShortageRequestsAreValidatedAndScoped(t *testing.T) {
	e := webhookEnv(t)
	for name, tc := range map[string]struct {
		path, body string
		id         *auth.Identity
		status     int
		code       string
	}{
		"wrong sku":       {"SHP-O-1", `{"skuId":"X","quantity":1,"reason":"r"}`, &hcmStaff, 422, "SKU_NOT_ON_PICK_LIST"},
		"too many":        {"SHP-O-1", `{"skuId":"SKU-A","quantity":9,"reason":"r"}`, &hcmStaff, 422, "QUANTITY_EXCEEDS_ORDER"},
		"no reason":       {"SHP-O-1", `{"skuId":"SKU-A","quantity":1}`, &hcmStaff, 400, "VALIDATION_FAILED"},
		"zero":            {"SHP-O-1", `{"skuId":"SKU-A","quantity":0,"reason":"r"}`, &hcmStaff, 400, "VALIDATION_FAILED"},
		"other warehouse": {"SHP-O-2", `{"skuId":"SKU-A","quantity":1,"reason":"r"}`, &hcmStaff, 403, "FORBIDDEN_WAREHOUSE_SCOPE"},
		"customer":        {"SHP-O-1", `{"skuId":"SKU-A","quantity":1,"reason":"r"}`, &customerOne, 403, "FORBIDDEN"},
	} {
		rec := e.call(t, "POST", "/api/v1/admin/shipments/"+tc.path+"/report-shortage", tc.body, tc.id)
		if rec.Code != tc.status || errCode(t, rec) != tc.code {
			t.Errorf("%s: %d %s", name, rec.Code, errCode(t, rec))
		}
	}
	if s, _ := e.store.Get(context.Background(), "SHP-O-1"); s.Status != shipment.StatusPendingPick {
		t.Fatalf("status = %s", s.Status)
	}
}

func TestACaseListIsScopedAndOpenToDeliveryCoordinators(t *testing.T) {
	e := webhookEnv(t)
	e.call(t, "POST", "/api/v1/admin/shipments/SHP-O-1/report-shortage", `{"skuId":"SKU-A","quantity":1,"reason":"r"}`, &hcmStaff)
	e.call(t, "POST", "/api/v1/admin/shipments/SHP-O-2/report-shortage", `{"skuId":"SKU-B","quantity":1,"reason":"r"}`, &hnStaff)
	if got := decode[[]caseRow](t, e.call(t, "GET", "/api/v1/admin/fulfillment-cases", "", &hcmStaff)); len(got) != 1 || got[0].ShipmentID != "SHP-O-1" {
		t.Fatalf("hcm cases = %+v", got)
	}
	if got := decode[[]caseRow](t, e.call(t, "GET", "/api/v1/admin/fulfillment-cases", "", &allManager)); len(got) != 2 {
		t.Fatalf("manager cases = %+v", got)
	}
	if got := decode[[]caseRow](t, e.call(t, "GET", "/api/v1/admin/fulfillment-cases?type=PICK_SHORTAGE", "", &coordinator)); len(got) != 2 {
		t.Fatalf("coordinator cases = %+v", got)
	}
	for _, c := range []struct {
		id   *auth.Identity
		q    string
		want int
	}{{nil, "", 401}, {&customerOne, "", 403}, {&allManager, "?type=NOPE", 400}, {&allManager, "?status=NOPE", 400}} {
		if rec := e.call(t, "GET", "/api/v1/admin/fulfillment-cases"+c.q, "", c.id); rec.Code != c.want {
			t.Errorf("%v %s: %d", c.id, c.q, rec.Code)
		}
	}
}

func TestAFailedDeliveryOpensACaseAndTriggersNothingElse(t *testing.T) {
	e := webhookEnv(t)
	dispatched(t, e)
	e.carrier(t, `{"eventId":"c-1","shipmentId":"SHP-O-1","type":"DELIVERY_FAILED","reason":"customer not reachable"}`, "sign")
	cases := decode[[]caseRow](t, e.call(t, "GET", "/api/v1/admin/fulfillment-cases", "", &hcmStaff))
	if len(cases) != 1 || cases[0].Type != "DELIVERY_FAILED" || cases[0].Reason != "customer not reachable" || cases[0].SkuBlocked {
		t.Fatalf("cases = %+v", cases)
	}
	for _, typ := range e.store.EventTypes("SHP-O-1") {
		if typ == "fulfillment.shipment_delivered.v1" {
			t.Fatal("a failed delivery completed the order")
		}
	}
}

func TestAPartialDeliveryDoesNotCompleteTheOrderOrStartTheWindow(t *testing.T) {
	e := webhookEnv(t)
	dispatched(t, e)
	body := `{"eventId":"c-1","shipmentId":"SHP-O-1","type":"PARTIALLY_DELIVERED","items":[{"skuId":"SKU-A","quantity":2}]}`
	for i := 0; i < 3; i++ {
		if rec := e.carrier(t, body, "sign"); rec.Code != 200 {
			t.Fatalf("partial %d: %d %s", i, rec.Code, rec.Body)
		}
	}
	s, _ := e.store.Get(context.Background(), "SHP-O-1")
	if s.Status != shipment.StatusPartiallyDelivered || s.Items[0].DeliveredQuantity != 2 || s.Items[1].DeliveredQuantity != 0 || s.ReturnWindowEndsAt != nil {
		t.Fatalf("shipment = %+v", s)
	}
	cases := decode[[]caseRow](t, e.call(t, "GET", "/api/v1/admin/fulfillment-cases?type=PARTIAL_DELIVERY", "", &hcmStaff))
	if len(cases) != 1 || cases[0].Quantity != 1 {
		t.Fatalf("cases = %+v", cases)
	}
	for _, typ := range e.store.EventTypes("SHP-O-1") {
		if typ == "fulfillment.shipment_delivered.v1" {
			t.Fatal("a partial delivery completed the order")
		}
	}
	// The remaining units arrive later: now it is complete.
	e.carrier(t, `{"eventId":"c-2","shipmentId":"SHP-O-1","type":"DELIVERED"}`, "sign")
	if s, _ := e.store.Get(context.Background(), "SHP-O-1"); s.Status != shipment.StatusDelivered || s.ReturnWindowEndsAt == nil {
		t.Fatalf("after completion: %+v", s)
	}
	e = webhookEnv(t)
	dispatched(t, e)
	for _, bad := range []string{
		`{"eventId":"c-3","shipmentId":"SHP-O-1","type":"PARTIALLY_DELIVERED"}`,
		`{"eventId":"c-4","shipmentId":"SHP-O-1","type":"PARTIALLY_DELIVERED","items":[{"skuId":"SKU-A","quantity":-1}]}`,
	} {
		if rec := e.carrier(t, bad, "sign"); rec.Code != 400 && rec.Code != 422 {
			t.Errorf("%s: %d", bad, rec.Code)
		}
	}
}
