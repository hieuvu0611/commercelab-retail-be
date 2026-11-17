package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/contracts/events"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth/authtest"
	"github.com/hieuvu0611/commercelab-retail-be/services/fulfillment-shipping-service/internal/intake"
	"github.com/hieuvu0611/commercelab-retail-be/services/fulfillment-shipping-service/internal/shipment"
	"github.com/hieuvu0611/commercelab-retail-be/services/fulfillment-shipping-service/internal/timeline"
)

func TestMultiParcelIntakeWarehouseScopeAndPartialDelivery(t *testing.T) {
	authtest.Configure(t)
	store := shipment.NewMemoryStore()
	tl := timeline.NewMemory()
	now := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)

	in := &intake.Intake{Store: store, Timeline: tl, Now: func() time.Time { return now }}
	err := in.Handle(context.Background(), events.DomainEvent{
		EventID:   "evt-multi-1",
		EventType: events.EventOrderConfirmed,
		Payload: map[string]any{
			"orderId":       "O-MULTI-1",
			"userId":        "user-lan",
			"warehouseId":   "MULTI",
			"paymentMethod": "COD",
			"parcels": []map[string]any{
				{
					"parcelSeq": 1, "totalParcels": 2, "warehouseId": "WH-HCM-01", "codAmountToCollect": 500000,
					"items": []map[string]any{{"skuId": "SKU-BALO-BLACK-20L", "quantity": 10}},
				},
				{
					"parcelSeq": 2, "totalParcels": 2, "warehouseId": "WH-HN-01", "codAmountToCollect": 160000,
					"items": []map[string]any{{"skuId": "SKU-BALO-BLACK-20L", "quantity": 2}},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("intake failed: %v", err)
	}

	parcels, err := store.ListByOrder(context.Background(), "O-MULTI-1")
	if err != nil || len(parcels) != 2 {
		t.Fatalf("expected 2 parcels, got len=%d err=%v", len(parcels), err)
	}
	if parcels[0].ShipmentID != "SHP-O-MULTI-1-1" || parcels[1].ShipmentID != "SHP-O-MULTI-1-2" {
		t.Fatalf("unexpected shipment ids: %s, %s", parcels[0].ShipmentID, parcels[1].ShipmentID)
	}

	srv := NewServer(Deps{Shipments: store, Timeline: tl, WebhookSecret: "test-webhook-secret-0123456789abcdef", Now: func() time.Time { return now }})

	// Staff scope check: HCM staff can view SHP-1, forbidden on SHP-2.
	hcmTok, _ := auth.SignAccessToken(auth.Identity{UserID: "staff-wh-hcm", Roles: []string{"WAREHOUSE_STAFF"}, WarehouseScopes: []string{"WH-HCM-01"}})
	r1 := httptest.NewRequest(http.MethodGet, "/api/v1/admin/shipments/SHP-O-MULTI-1-1", nil)
	r1.Header.Set("Authorization", "Bearer "+hcmTok)
	rec1 := httptest.NewRecorder()
	srv.ServeHTTP(rec1, r1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("HCM staff on parcel 1 = %d", rec1.Code)
	}
	r2 := httptest.NewRequest(http.MethodGet, "/api/v1/admin/shipments/SHP-O-MULTI-1-2", nil)
	r2.Header.Set("Authorization", "Bearer "+hcmTok)
	rec2 := httptest.NewRecorder()
	srv.ServeHTTP(rec2, r2)
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("HCM staff on parcel 2 = %d, want 403", rec2.Code)
	}

	// Mark parcel 1 DELIVERED and parcel 2 IN_TRANSIT -> aggregate tracking is PARTIALLY_DELIVERED
	p1 := parcels[0]
	p1.Status = shipment.StatusDelivered
	p1.Version++
	_ = store.Save(context.Background(), p1, parcels[0].Version)
	p2 := parcels[1]
	p2.Status = shipment.StatusInTransit
	p2.Version++
	_ = store.Save(context.Background(), p2, parcels[1].Version)

	custTok, _ := auth.SignAccessToken(auth.Identity{UserID: "user-lan", Roles: []string{"CUSTOMER"}})
	tReq := httptest.NewRequest(http.MethodGet, "/api/v1/orders/O-MULTI-1/tracking", nil)
	tReq.Header.Set("Authorization", "Bearer "+custTok)
	tRec := httptest.NewRecorder()
	srv.ServeHTTP(tRec, tReq)
	if tRec.Code != http.StatusOK {
		t.Fatalf("tracking status = %d: %s", tRec.Code, tRec.Body.String())
	}
	var tr struct {
		Data struct {
			Status  string `json:"status"`
			Parcels []any  `json:"parcels"`
		} `json:"data"`
	}
	_ = json.Unmarshal(tRec.Body.Bytes(), &tr)
	if tr.Data.Status != "PARTIALLY_DELIVERED" || len(tr.Data.Parcels) != 2 {
		t.Fatalf("expected PARTIALLY_DELIVERED with 2 parcels, got %+v", tr.Data)
	}
}
