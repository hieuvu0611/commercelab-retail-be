package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/contracts/events"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/internalauth"
	"github.com/hieuvu0611/commercelab-retail-be/services/fulfillment-shipping-service/internal/intake"
	"github.com/hieuvu0611/commercelab-retail-be/services/fulfillment-shipping-service/internal/shipment"
)

const stopToken = "test-internal-token-0123456789abcdef"

type stopResp struct {
	Result         string `json:"result"`
	ShipmentID     string `json:"shipmentId"`
	ShipmentStatus string `json:"shipmentStatus"`
}

const stopBody = `{"userId":"user-1","warehouseId":"WH-HCM-01","items":[{"skuId":"SKU-A","quantity":2},{"skuId":"SKU-B","quantity":1}]}`

// stop calls the internal stop route; internal=false sends no token.
func (e *env) stop(t *testing.T, orderID, body string, internal bool) *httptest.ResponseRecorder {
	t.Helper()
	_ = internalauth.Configure(stopToken)
	t.Cleanup(internalauth.Reset)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/shipments/internal/orders/"+orderID+"/stop", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if internal {
		req.Header.Set(internalauth.Header, stopToken)
	}
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	return rec
}

func countOf(types []string, want string) int {
	n := 0
	for _, ty := range types {
		if ty == want {
			n++
		}
	}
	return n
}

func TestStopRouteIsInternalOnly(t *testing.T) {
	e := newEnv(t)
	if rec := e.stop(t, "O-1", stopBody, false); rec.Code != http.StatusUnauthorized || errCode(t, rec) != "INTERNAL_AUTH_REQUIRED" {
		t.Fatalf("no token = %d %s", rec.Code, rec.Body)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/shipments/internal/orders/O-1/stop", strings.NewReader(stopBody))
	tok, _ := auth.SignAccessToken(allManager)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("a staff token must not stop a parcel through the internal route: %d", rec.Code)
	}
	if s, _ := e.store.Get(context.Background(), "SHP-O-1"); s.Status != shipment.StatusPendingPick {
		t.Fatalf("status = %s", s.Status)
	}
}

func TestStopOfAnUnpickedParcelStopsItOnceAndBlocksHandover_Case2(t *testing.T) {
	stock := &fakeStock{}
	e := handoverEnv(t, stock)
	for i := 0; i < 3; i++ {
		rec := e.stop(t, "O-1", stopBody, true)
		got := decode[stopResp](t, rec)
		if rec.Code != 200 || got.Result != "STOPPED" || got.ShipmentStatus != "STOPPED_FOR_CANCEL" || got.ShipmentID != "SHP-O-1" {
			t.Fatalf("stop #%d = %d %s", i, rec.Code, rec.Body)
		}
	}
	s, _ := e.store.Get(context.Background(), "SHP-O-1")
	if s.Status != shipment.StatusStoppedForCancel || s.Version != 2 {
		t.Fatalf("shipment = %+v, want stopped at version 2 (one change)", s)
	}
	if n := countOf(e.store.EventTypes("SHP-O-1"), events.EventShipmentStopped); n != 1 {
		t.Fatalf("stopped events = %d, want 1", n)
	}
	if rec := e.call(t, "POST", "/api/v1/admin/shipments/SHP-O-1/scan", `{"skuId":"SKU-A","quantity":1}`, &hcmStaff); rec.Code != 409 || errCode(t, rec) != "SHIPMENT_NOT_PICKABLE" {
		t.Fatalf("scan after stop = %d %s", rec.Code, rec.Body)
	}
	if rec := e.call(t, "POST", "/api/v1/admin/shipments/SHP-O-1/handover", `{}`, &hcmStaff); rec.Code != 409 || errCode(t, rec) != "HANDOVER_NOT_ALLOWED" {
		t.Fatalf("handover after stop = %d %s", rec.Code, rec.Body)
	}
	if len(stock.calls) != 0 {
		t.Fatalf("inventory handover was called for a stopped parcel: %v", stock.calls)
	}
	if entries, _ := e.tl.Get(context.Background(), "SHP-O-1"); len(entries) == 0 || entries[len(entries)-1].Type != "STOPPED_FOR_CANCEL" {
		t.Fatalf("timeline = %+v", entries)
	}
}

func TestStopWhilePickingRequestsAndAfterHandoverChangesNothing(t *testing.T) {
	stock := &fakeStock{}
	e := handoverEnv(t, stock)
	e.call(t, "POST", "/api/v1/admin/shipments/SHP-O-1/scan", `{"skuId":"SKU-A","quantity":1}`, &hcmStaff)
	if got := decode[stopResp](t, e.stop(t, "O-1", stopBody, true)); got.Result != "STOP_REQUESTED" || got.ShipmentStatus != "PICKING" {
		t.Fatalf("picking = %+v", got)
	}
	pack(t, e, "SHP-O-2", &hnStaff)
	e.call(t, "POST", "/api/v1/admin/shipments/SHP-O-2/handover", `{}`, &hnStaff)
	before, _ := e.store.Get(context.Background(), "SHP-O-2")
	if got := decode[stopResp](t, e.stop(t, "O-2", stopBody, true)); got.Result != "TOO_LATE" || got.ShipmentStatus != "HANDED_OVER" {
		t.Fatalf("handed over = %+v", got)
	}
	if after, _ := e.store.Get(context.Background(), "SHP-O-2"); after.Version != before.Version || after.Status != shipment.StatusHandedOver {
		t.Fatalf("a refused stop changed the parcel: %+v", after)
	}
}

func TestStopBeforeTheParcelExistsBlocksTheLateOrderConfirmedEvent(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 2; i++ {
		rec := e.stop(t, "O-NEW", stopBody, true)
		if got := decode[stopResp](t, rec); rec.Code != 200 || got.Result != "STOPPED" || got.ShipmentStatus != "STOPPED_FOR_CANCEL" {
			t.Fatalf("stop #%d = %d %s", i, rec.Code, rec.Body)
		}
	}
	in := &intake.Intake{Store: e.store, Now: func() time.Time { return time.Now().UTC() }}
	confirmed := events.DomainEvent{EventID: "evt-late", EventType: events.EventOrderConfirmed, AggregateType: "Order", AggregateID: "O-NEW", Payload: map[string]any{
		"orderId": "O-NEW", "userId": "user-1", "warehouseId": "WH-HCM-01", "items": []map[string]any{{"skuId": "SKU-A", "quantity": 2}}}}
	if err := in.Handle(context.Background(), confirmed); err != nil {
		t.Fatal(err)
	}
	s, err := e.store.GetByOrder(context.Background(), "O-NEW")
	if err != nil || s.Status != shipment.StatusStoppedForCancel {
		t.Fatalf("shipment = %+v %v, want it to stay stopped", s, err)
	}
	if n := countOf(e.store.EventTypes(s.ShipmentID), events.EventShipmentCreated); n != 0 {
		t.Fatalf("a created event was published for a cancelled order")
	}
}

func TestStopOfAnUnknownParcelNeedsTheOrdersDetails(t *testing.T) {
	e := newEnv(t)
	for _, body := range []string{`{}`, `{"userId":"u","warehouseId":"WH-HCM-01"}`, `{"userId":"u","items":[{"skuId":"A","quantity":1}]}`, `{"userId":"u","warehouseId":"WH-HCM-01","items":[{"skuId":"A","quantity":0}]}`} {
		if rec := e.stop(t, "O-NOPE", body, true); rec.Code != 400 || errCode(t, rec) != "VALIDATION_FAILED" {
			t.Errorf("%s = %d %s", body, rec.Code, rec.Body)
		}
	}
	// An existing parcel needs none of the order's details.
	if rec := e.stop(t, "O-1", `{}`, true); rec.Code != 200 {
		t.Errorf("existing parcel, empty details = %d %s", rec.Code, rec.Body)
	}
}

func TestConcurrentStopsAndScansLeaveOneConsistentParcel(t *testing.T) {
	for round := 0; round < 20; round++ {
		e := newEnv(t)
		var wg sync.WaitGroup
		results := make([]string, 4)
		for i := range results {
			wg.Add(1)
			go func() {
				defer wg.Done()
				results[i] = decode[stopResp](t, e.stop(t, "O-1", stopBody, true)).Result
			}()
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.call(t, "POST", "/api/v1/admin/shipments/SHP-O-1/scan", `{"skuId":"SKU-A","quantity":1}`, &hcmStaff)
		}()
		wg.Wait()
		s, _ := e.store.Get(context.Background(), "SHP-O-1")
		switch s.Status {
		case shipment.StatusStoppedForCancel:
			if s.Items[0].PickedQuantity != 0 {
				t.Fatalf("a stopped parcel was also picked: %+v", s)
			}
		case shipment.StatusPicking: // the scan won; the stop became a request the warehouse must confirm
		default:
			t.Fatalf("status = %s", s.Status)
		}
		if n := countOf(e.store.EventTypes("SHP-O-1"), events.EventShipmentStopped); (s.Status == shipment.StatusStoppedForCancel) != (n == 1) || n > 1 {
			t.Fatalf("status %s with %d stopped events", s.Status, n)
		}
	}
}

func TestStopForCancelHandlesAllParcelsOfSplitOrder(t *testing.T) {
	stock := &fakeStock{}
	e := handoverEnv(t, stock)
	// Replace store with a 2-parcel split order O-SPLIT2
	for idx, wh := range []string{"WH-HCM-01", "WH-HN-01"} {
		s, _ := shipment.NewParcel("O-SPLIT2", "user-1", wh, "PREPAID", 0, idx+1, 2,
			[]shipment.Item{{SkuID: "SKU-A", Quantity: 1}}, time.Now())
		_, _, _ = e.store.Create(context.Background(), s, "evt-sp2-"+wh)
	}
	rec := e.stop(t, "O-SPLIT2", stopBody, true)
	got := decode[stopResp](t, rec)
	if rec.Code != 200 || got.Result != "STOPPED" {
		t.Fatalf("split stop = %d %+v", rec.Code, got)
	}
	for _, id := range []string{"SHP-O-SPLIT2-1", "SHP-O-SPLIT2-2"} {
		s, _ := e.store.Get(context.Background(), id)
		if s.Status != shipment.StatusStoppedForCancel {
			t.Fatalf("%s status = %s, want STOPPED_FOR_CANCEL", id, s.Status)
		}
	}

	// If second parcel of a split order was already handed over, stop returns TOO_LATE and stops neither parcel
	for idx, wh := range []string{"WH-HCM-01", "WH-HN-01"} {
		s, _ := shipment.NewParcel("O-SPLIT3", "user-1", wh, "PREPAID", 0, idx+1, 2,
			[]shipment.Item{{SkuID: "SKU-A", Quantity: 1}}, time.Now())
		if idx == 1 {
			s, _ = s.Scan("SKU-A", 1, time.Now())
			s, _ = s.Pack(time.Now())
			s, _ = s.Handover(time.Now())
		}
		_, _, _ = e.store.Create(context.Background(), s, "evt-sp3-"+wh)
	}
	recLate := e.stop(t, "O-SPLIT3", stopBody, true)
	gotLate := decode[stopResp](t, recLate)
	if recLate.Code != 200 || gotLate.Result != "TOO_LATE" {
		t.Fatalf("late split stop = %d %+v", recLate.Code, gotLate)
	}
	p1, _ := e.store.Get(context.Background(), "SHP-O-SPLIT3-1")
	if p1.Status != shipment.StatusPendingPick {
		t.Fatalf("parcel 1 mutated despite parcel 2 handed over: %s", p1.Status)
	}
}

func TestSplitStopDoesNotReleaseStockWhenSiblingHandoverWins(t *testing.T) {
	stock := &fakeStock{}
	e := handoverEnv(t, stock)
	ctx := context.Background()
	for index, warehouse := range []string{"WH-HCM-01", "WH-HN-01"} {
		parcel, _ := shipment.NewParcel("O-SPLIT-RACE", "user-1", warehouse, "PREPAID", 0, index+1, 2,
			[]shipment.Item{{SkuID: "SKU-A", Quantity: 1}}, time.Now())
		if index == 1 {
			parcel, _ = parcel.Scan("SKU-A", 1, time.Now())
			parcel, _ = parcel.Pack(time.Now())
		}
		if _, _, err := e.store.Create(ctx, parcel, "test-"+warehouse); err != nil {
			t.Fatal(err)
		}
	}
	rec := e.stop(t, "O-SPLIT-RACE", stopBody, true)
	if got := decode[stopResp](t, rec); rec.Code != 200 || got.Result != "STOP_REQUESTED" || len(stock.released) != 0 {
		t.Fatalf("initial split stop = %d %+v, releases %v", rec.Code, got, stock.released)
	}
	handed := e.call(t, "POST", "/api/v1/admin/shipments/SHP-O-SPLIT-RACE-2/handover", `{}`, &hnStaff)
	if handed.Code != 200 {
		t.Fatalf("handover: %d %s", handed.Code, handed.Body)
	}
	late := e.stop(t, "O-SPLIT-RACE", stopBody, true)
	if got := decode[stopResp](t, late); late.Code != 200 || got.Result != "TOO_LATE" || len(stock.released) != 0 {
		t.Fatalf("late split stop = %d %+v, releases %v", late.Code, got, stock.released)
	}
	first, err := e.store.Get(ctx, "SHP-O-SPLIT-RACE-1")
	if err != nil || first.Status != shipment.StatusPendingPick || first.StopConfirmedAt != nil {
		t.Fatalf("unpicked sibling should resume work: %+v, %v", first, err)
	}
}

func TestSplitStopBeforeShipmentIntakeUsesRealWarehouses(t *testing.T) {
	stock := &fakeStock{}
	e := handoverEnv(t, stock)
	rec := e.stop(t, "O-SPLIT-NEW", `{"userId":"user-1","warehouseId":"MULTI","paymentMethod":"PREPAID","parcels":[
		{"parcelSeq":1,"warehouseId":"WH-HCM-01","items":[{"skuId":"SKU-A","quantity":1}]},
		{"parcelSeq":2,"warehouseId":"WH-HN-01","items":[{"skuId":"SKU-B","quantity":1}]}
	]}`, true)
	if got := decode[stopResp](t, rec); rec.Code != 200 || got.Result != "STOPPED" || len(stock.released) != 1 {
		t.Fatalf("stop before intake: %d %+v releases=%v", rec.Code, got, stock.released)
	}
	list, err := e.store.ListByOrder(context.Background(), "O-SPLIT-NEW")
	if err != nil || len(list) != 2 || list[0].WarehouseID != "WH-HCM-01" || list[1].WarehouseID != "WH-HN-01" {
		t.Fatalf("stopped warehouses: %+v %v", list, err)
	}
	for _, parcel := range list {
		if parcel.Status != shipment.StatusStoppedForCancel {
			t.Fatalf("parcel was not stopped: %+v", parcel)
		}
	}
}
