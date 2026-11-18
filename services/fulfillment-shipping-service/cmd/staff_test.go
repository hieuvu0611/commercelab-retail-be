package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth/authtest"
	"github.com/hieuvu0611/commercelab-retail-be/services/fulfillment-shipping-service/internal/shipment"
	"github.com/hieuvu0611/commercelab-retail-be/services/fulfillment-shipping-service/internal/timeline"
)

var (
	hcmStaff    = auth.Identity{UserID: "staff-hcm", Roles: []string{"WAREHOUSE_STAFF"}, WarehouseScopes: []string{"WH-HCM-01"}}
	hnStaff     = auth.Identity{UserID: "staff-hn", Roles: []string{"WAREHOUSE_STAFF"}, WarehouseScopes: []string{"WH-HN-01"}}
	allManager  = auth.Identity{UserID: "mgr-all", Roles: []string{"WAREHOUSE_MGR"}, WarehouseScopes: []string{"ALL"}}
	customerOne = auth.Identity{UserID: "user-1", Roles: []string{"CUSTOMER"}}
)

type env struct {
	srv   http.Handler
	store *shipment.MemoryStore
	tl    *timeline.Memory
}

func newEnv(t *testing.T) *env {
	t.Helper()
	authtest.Configure(t)
	store := shipment.NewMemoryStore()
	seed := func(order, wh string) {
		s, err := shipment.New(order, "user-1", wh, []shipment.Item{{SkuID: "SKU-A", Quantity: 2}, {SkuID: "SKU-B", Quantity: 1}}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.Create(context.Background(), s, "evt-"+order); err != nil {
			t.Fatal(err)
		}
	}
	seed("O-1", "WH-HCM-01")
	seed("O-2", "WH-HN-01")
	tl := timeline.NewMemory()
	return &env{srv: NewServer(Deps{Shipments: store, Timeline: tl}), store: store, tl: tl}
}

func (e *env) call(t *testing.T, method, path, body string, id *auth.Identity) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if id != nil {
		tok, err := auth.SignAccessToken(*id)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var env struct {
		Data T `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return env.Data
}

func errCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return body.Code
}

func TestStaffRoutesNeedAWarehouseRole(t *testing.T) {
	e := newEnv(t)
	for _, tc := range []struct {
		id   *auth.Identity
		want int
	}{{nil, 401}, {&customerOne, 403}, {&hcmStaff, 200}, {&allManager, 200}} {
		if rec := e.call(t, "GET", "/api/v1/admin/shipments", "", tc.id); rec.Code != tc.want {
			t.Errorf("identity %v: status %d, want %d", tc.id, rec.Code, tc.want)
		}
	}
}

func TestTheQueueShowsOnlyTheStaffMembersWarehouses(t *testing.T) {
	e := newEnv(t)
	hcm := decode[[]shipmentView](t, e.call(t, "GET", "/api/v1/admin/shipments", "", &hcmStaff))
	if len(hcm) != 1 || hcm[0].OrderID != "O-1" {
		t.Fatalf("hcm queue = %+v", hcm)
	}
	all := decode[[]shipmentView](t, e.call(t, "GET", "/api/v1/admin/shipments", "", &allManager))
	if len(all) != 2 {
		t.Fatalf("manager queue = %+v", all)
	}
	if got := decode[[]shipmentView](t, e.call(t, "GET", "/api/v1/admin/shipments?status=PICKING", "", &allManager)); len(got) != 0 {
		t.Fatalf("picking queue = %+v", got)
	}
	for _, q := range []string{"status=NOPE", "page=0", "limit=101"} {
		if rec := e.call(t, "GET", "/api/v1/admin/shipments?"+q, "", &allManager); rec.Code != 400 {
			t.Errorf("%s: status %d", q, rec.Code)
		}
	}
}

func TestStaffCannotTouchAnotherWarehousesShipment(t *testing.T) {
	e := newEnv(t)
	for _, call := range [][2]string{
		{"GET", ""}, {"POST", "/scan"}, {"POST", "/pack"},
	} {
		rec := e.call(t, call[0], "/api/v1/admin/shipments/SHP-O-2"+call[1], `{"skuId":"SKU-A","quantity":1}`, &hcmStaff)
		if rec.Code != 403 || errCode(t, rec) != "FORBIDDEN_WAREHOUSE_SCOPE" {
			t.Errorf("%v: status %d code %s", call, rec.Code, errCode(t, rec))
		}
	}
	if rec := e.call(t, "GET", "/api/v1/admin/shipments/SHP-NOPE", "", &hnStaff); rec.Code != 404 || errCode(t, rec) != "SHIPMENT_NOT_FOUND" {
		t.Fatalf("missing: %d %s", rec.Code, errCode(t, rec))
	}
}

func TestPickScanAndPackHappyPath(t *testing.T) {
	e := newEnv(t)
	rec := e.call(t, "POST", "/api/v1/admin/shipments/SHP-O-1/scan", `{"skuId":"SKU-A","quantity":2,"version":1}`, &hcmStaff)
	if rec.Code != 200 {
		t.Fatalf("scan: %d %s", rec.Code, rec.Body)
	}
	v := decode[shipmentView](t, rec)
	if v.Status != "PICKING" || v.Version != 2 || v.Items[0].PickedQuantity != 2 {
		t.Fatalf("after scan = %+v", v)
	}
	if rec := e.call(t, "POST", "/api/v1/admin/shipments/SHP-O-1/pack", `{}`, &hcmStaff); rec.Code != 409 || errCode(t, rec) != "PICK_INCOMPLETE" {
		t.Fatalf("early pack: %d %s", rec.Code, errCode(t, rec))
	}
	e.call(t, "POST", "/api/v1/admin/shipments/SHP-O-1/scan", `{"skuId":"SKU-B","quantity":1}`, &hcmStaff)
	rec = e.call(t, "POST", "/api/v1/admin/shipments/SHP-O-1/pack", `{"version":3}`, &hcmStaff)
	if v := decode[shipmentView](t, rec); rec.Code != 200 || v.Status != "PACKED" || v.Version != 4 {
		t.Fatalf("pack: %d %+v", rec.Code, v)
	}
	if rec := e.call(t, "GET", "/api/v1/admin/shipments/SHP-O-1", "", &hcmStaff); decode[shipmentView](t, rec).Status != "PACKED" {
		t.Fatal("detail does not show PACKED")
	}
}

func TestScanErrorsUseStableCodes(t *testing.T) {
	e := newEnv(t)
	for name, tc := range map[string]struct {
		body   string
		status int
		code   string
	}{
		"wrong sku":     {`{"skuId":"SKU-X","quantity":1}`, 422, "SKU_NOT_ON_PICK_LIST"},
		"over quantity": {`{"skuId":"SKU-B","quantity":2}`, 422, "QUANTITY_EXCEEDS_ORDER"},
		"zero quantity": {`{"skuId":"SKU-B","quantity":0}`, 400, "VALIDATION_FAILED"},
		"no sku":        {`{"quantity":1}`, 400, "VALIDATION_FAILED"},
		"stale version": {`{"skuId":"SKU-B","quantity":1,"version":7}`, 409, "STALE_VERSION"},
		"bad json":      {`{`, 400, "INVALID_JSON"},
	} {
		rec := e.call(t, "POST", "/api/v1/admin/shipments/SHP-O-1/scan", tc.body, &hcmStaff)
		if rec.Code != tc.status || errCode(t, rec) != tc.code {
			t.Errorf("%s: %d %s, want %d %s", name, rec.Code, errCode(t, rec), tc.status, tc.code)
		}
	}
	// Nothing above changed the parcel.
	if s, _ := e.store.Get(context.Background(), "SHP-O-1"); s.Version != 1 {
		t.Fatalf("version = %d", s.Version)
	}
	e.call(t, "POST", "/api/v1/admin/shipments/SHP-O-1/scan", `{"skuId":"SKU-A","quantity":2}`, &hcmStaff)
	e.call(t, "POST", "/api/v1/admin/shipments/SHP-O-1/scan", `{"skuId":"SKU-B","quantity":1}`, &hcmStaff)
	e.call(t, "POST", "/api/v1/admin/shipments/SHP-O-1/pack", `{}`, &hcmStaff)
	if rec := e.call(t, "POST", "/api/v1/admin/shipments/SHP-O-1/scan", `{"skuId":"SKU-A","quantity":1}`, &hcmStaff); rec.Code != 409 || errCode(t, rec) != "SHIPMENT_NOT_PICKABLE" {
		t.Fatalf("scan after pack: %d %s", rec.Code, errCode(t, rec))
	}
}

func TestListOrderShipmentsRespectsWarehouseScopeAndStaffDeliverActions(t *testing.T) {
	authtest.Configure(t)
	store := shipment.NewMemoryStore()
	for idx, wh := range []string{"WH-HCM-01", "WH-HN-01"} {
		s, err := shipment.NewParcel("O-SPLIT", "user-1", wh, "COD", 250000, idx+1, 2,
			[]shipment.Item{{SkuID: "SKU-A", Quantity: 1}}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		// Progress both parcels to HANDED_OVER
		s, _ = s.Scan("SKU-A", 1, time.Now())
		s, _ = s.Pack(time.Now())
		s, _ = s.Handover(time.Now())
		if _, _, err := store.Create(context.Background(), s, "evt-split-"+wh); err != nil {
			t.Fatal(err)
		}
	}
	e := &env{srv: NewServer(Deps{Shipments: store, Timeline: timeline.NewMemory()}), store: store, tl: timeline.NewMemory()}

	// HCM staff only sees HCM parcel on listOrderShipments
	hcmParcels := decode[[]shipmentView](t, e.call(t, "GET", "/api/v1/admin/orders/O-SPLIT/shipments", "", &hcmStaff))
	if len(hcmParcels) != 1 || hcmParcels[0].WarehouseID != "WH-HCM-01" {
		t.Fatalf("expected only WH-HCM-01 parcel, got %+v", hcmParcels)
	}

	// All-warehouse manager sees both parcels
	allParcels := decode[[]shipmentView](t, e.call(t, "GET", "/api/v1/admin/orders/O-SPLIT/shipments", "", &allManager))
	if len(allParcels) != 2 {
		t.Fatalf("expected 2 parcels for ALL scope, got %+v", allParcels)
	}

	// Delivery coordinator and ops manager without explicit warehouseScopes (matching iam-auth-service JWTs) see both parcels
	deliveryCoord := auth.Identity{UserID: "staff-delivery-coord", Roles: []string{"DELIVERY_COORD"}}
	coordParcels := decode[[]shipmentView](t, e.call(t, "GET", "/api/v1/admin/orders/O-SPLIT/shipments", "", &deliveryCoord))
	if len(coordParcels) != 2 {
		t.Fatalf("expected 2 parcels for DELIVERY_COORD without warehouseScopes, got %+v", coordParcels)
	}

	// Staff marks parcel 1 delivered and parcel 2 failed
	d1 := e.call(t, "POST", "/api/v1/admin/shipments/SHP-O-SPLIT-1/deliver", `{}`, &deliveryCoord)
	if v := decode[shipmentView](t, d1); d1.Code != 200 || v.Status != "DELIVERED" {
		t.Fatalf("deliver parcel 1 as DELIVERY_COORD = %d %+v", d1.Code, v)
	}
	f2 := e.call(t, "POST", "/api/v1/admin/shipments/SHP-O-SPLIT-2/delivery-failed", `{"reason":"Recipient unreachable"}`, &hnStaff)
	if v := decode[shipmentView](t, f2); f2.Code != 200 || v.Status != "DELIVERY_FAILED" {
		t.Fatalf("fail parcel 2 = %d %+v", f2.Code, v)
	}
}
