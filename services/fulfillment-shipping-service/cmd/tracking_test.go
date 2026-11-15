package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/services/fulfillment-shipping-service/internal/timeline"
)

type trackingResp struct {
	OrderID            string `json:"orderId"`
	ShipmentID         string `json:"shipmentId"`
	Status             string `json:"status"`
	HandedOverAt       string `json:"handedOverAt"`
	DeliveredAt        string `json:"deliveredAt"`
	ReturnWindowEndsAt string `json:"returnWindowEndsAt"`
	Quantities         []struct {
		SkuID      string `json:"skuId"`
		Ordered    int64  `json:"ordered"`
		Dispatched int64  `json:"dispatched"`
		Delivered  int64  `json:"delivered"`
		Returned   int64  `json:"returned"`
	} `json:"quantities"`
	Timeline []struct {
		At      string `json:"at"`
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"timeline"`
}

func timelineTypes(v trackingResp) string {
	out := make([]string, len(v.Timeline))
	for i, e := range v.Timeline {
		out[i] = e.Type
	}
	return strings.Join(out, ",")
}

func TestTrackingShowsQuantitiesAndTheTimelineOfTheWholeJourney(t *testing.T) {
	e := webhookEnv(t)
	const path = "/api/v1/orders/O-1/tracking"
	pack(t, e, "SHP-O-1", &hcmStaff)
	if v := decode[trackingResp](t, e.call(t, "GET", path, "", &customerOne)); v.Status != "PACKED" || v.Quantities[0].Ordered != 2 || v.Quantities[0].Dispatched != 0 {
		t.Fatalf("packed view = %+v", v)
	}
	e.call(t, "POST", "/api/v1/admin/shipments/SHP-O-1/handover", `{}`, &hcmStaff)
	e.carrier(t, `{"eventId":"c-1","shipmentId":"SHP-O-1","type":"IN_TRANSIT"}`, "sign")
	for i := 0; i < 3; i++ {
		e.carrier(t, `{"eventId":"c-2","shipmentId":"SHP-O-1","type":"DELIVERED"}`, "sign")
	}
	rec := e.call(t, "GET", path, "", &customerOne)
	v := decode[trackingResp](t, rec)
	if rec.Code != 200 || v.OrderID != "O-1" || v.ShipmentID != "SHP-O-1" || v.Status != "DELIVERED" || v.DeliveredAt == "" || v.ReturnWindowEndsAt == "" || v.HandedOverAt == "" {
		t.Fatalf("view = %d %+v", rec.Code, v)
	}
	if got := timelineTypes(v); got != "PICKING_STARTED,PACKED,HANDED_OVER,IN_TRANSIT,DELIVERED" {
		t.Fatalf("timeline = %s", got)
	}
	q := v.Quantities[0]
	if q.SkuID != "SKU-A" || q.Ordered != 2 || q.Dispatched != 2 || q.Delivered != 2 || q.Returned != 0 {
		t.Fatalf("quantities = %+v", v.Quantities)
	}
	for _, leak := range []string{"warehouse", "WH-HCM", "version", "staff-hcm"} {
		if strings.Contains(rec.Body.String(), leak) {
			t.Errorf("tracking leaks %q: %s", leak, rec.Body)
		}
	}
}

func TestTrackingIsOwnerOnly(t *testing.T) {
	e := webhookEnv(t)
	other := auth.Identity{UserID: "user-2", Roles: []string{"CUSTOMER"}}
	for name, tc := range map[string]struct {
		path string
		id   *auth.Identity
		want int
	}{
		"owner":         {"/api/v1/orders/O-1/tracking", &customerOne, 200},
		"another user":  {"/api/v1/orders/O-1/tracking", &other, 404},
		"unknown order": {"/api/v1/orders/O-404/tracking", &customerOne, 404},
		"anonymous":     {"/api/v1/orders/O-1/tracking", nil, 401},
		"staff":         {"/api/v1/orders/O-1/tracking", &hcmStaff, 403},
	} {
		if rec := e.call(t, "GET", tc.path, "", tc.id); rec.Code != tc.want {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if rec := e.call(t, "GET", "/api/v1/orders/O-1/tracking", "", &other); errCode(t, rec) != "TRACKING_NOT_FOUND" {
		t.Fatalf("code = %s", errCode(t, rec))
	}
}

func TestTrackingHidesInternalShortageReasonsButShowsDeliveryFailures(t *testing.T) {
	e := webhookEnv(t)
	e.call(t, "POST", "/api/v1/admin/shipments/SHP-O-1/report-shortage", `{"skuId":"SKU-A","quantity":1,"reason":"secret shelf note"}`, &hcmStaff)
	rec := e.call(t, "GET", "/api/v1/orders/O-1/tracking", "", &customerOne)
	if strings.Contains(rec.Body.String(), "secret shelf note") || !strings.Contains(timelineTypes(decode[trackingResp](t, rec)), "PICK_BLOCKED") {
		t.Fatalf("shortage view = %s", rec.Body)
	}

	e = webhookEnv(t)
	dispatched(t, e)
	e.carrier(t, `{"eventId":"c-1","shipmentId":"SHP-O-1","type":"DELIVERY_FAILED","reason":"customer not reachable"}`, "sign")
	rec = e.call(t, "GET", "/api/v1/orders/O-1/tracking", "", &customerOne)
	if v := decode[trackingResp](t, rec); v.Status != "DELIVERY_FAILED" || !strings.Contains(rec.Body.String(), "customer not reachable") || v.DeliveredAt != "" {
		t.Fatalf("failed view = %s", rec.Body)
	}
}

func TestTheTimelineIsNotDuplicatedByRepeatedRequests(t *testing.T) {
	e := webhookEnv(t)
	dispatched(t, e)
	for i := 0; i < 3; i++ {
		e.call(t, "POST", "/api/v1/admin/shipments/SHP-O-1/handover", `{}`, &hcmStaff)
		e.call(t, "POST", "/api/v1/admin/shipments/SHP-O-1/pack", `{}`, &hcmStaff)
	}
	entries, _ := e.tl.Get(context.Background(), "SHP-O-1")
	var handovers int
	for _, en := range entries {
		if en.Type == timeline.TypeHandedOver {
			handovers++
		}
	}
	if handovers != 1 {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestATimelineOutageAnswers503ButDoesNotBreakStaffActions(t *testing.T) {
	e := webhookEnv(t)
	e.srv = NewServer(Deps{Shipments: e.store, Stock: &fakeStock{}, WebhookSecret: webhookSecret, Timeline: brokenTimeline{}})
	if rec := e.call(t, "POST", "/api/v1/admin/shipments/SHP-O-1/scan", `{"skuId":"SKU-A","quantity":1}`, &hcmStaff); rec.Code != 200 {
		t.Fatalf("scan with a broken timeline: %d %s", rec.Code, rec.Body)
	}
	if rec := e.call(t, "GET", "/api/v1/orders/O-1/tracking", "", &customerOne); rec.Code != 503 {
		t.Fatalf("tracking with a broken timeline: %d", rec.Code)
	}
}

var dependencyDown = dependency.Wrap(errors.New("mongo down"))

type brokenTimeline struct{}

func (brokenTimeline) Append(context.Context, string, string, timeline.Entry) error {
	return dependencyDown
}
func (brokenTimeline) Get(context.Context, string) ([]timeline.Entry, error) {
	return nil, dependencyDown
}
