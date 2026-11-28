package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/contracts/events"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth/authtest"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/checkout"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/orders"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/snapshot"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/voucher"
)

type fakePOSCatalog struct {
	prices map[string]checkout.SKUPricing
	calls  atomic.Int32
}

func (f *fakePOSCatalog) GetSKUPrice(_ context.Context, skuID string) (checkout.SKUPricing, error) {
	f.calls.Add(1)
	p, ok := f.prices[skuID]
	if !ok {
		return checkout.SKUPricing{}, checkout.ErrInvalidSKU
	}
	return p, nil
}

type fakePOSStock struct {
	reserves, commits, handovers, releases, committedReleases atomic.Int32
	reserveErr, commitErr, handoverErr                        error
	lastWH                                                    string
}

func (f *fakePOSStock) Reserve(_ context.Context, _ string, warehouseID string, _ []checkout.StockLine) error {
	f.reserves.Add(1)
	f.lastWH = warehouseID
	return f.reserveErr
}

func (f *fakePOSStock) Release(context.Context, string) error {
	f.releases.Add(1)
	return nil
}

func (f *fakePOSStock) Commit(context.Context, string) error {
	f.commits.Add(1)
	return f.commitErr
}

func (f *fakePOSStock) ExceptionHold(context.Context, string) error { return nil }

func (f *fakePOSStock) ReleaseCommitted(context.Context, string) error {
	f.committedReleases.Add(1)
	return nil
}

func (f *fakePOSStock) Handover(context.Context, string, string) error {
	f.handovers.Add(1)
	return f.handoverErr
}

type posFixture struct {
	orders  *orders.MemoryStore
	snaps   *snapshot.MemoryStore
	catalog *fakePOSCatalog
	stock   *fakePOSStock
	srv     http.Handler
}

func newPOSFixture(t *testing.T) *posFixture {
	t.Helper()
	authtest.Configure(t)
	f := &posFixture{
		orders: orders.NewMemoryStore(),
		snaps:  snapshot.NewMemoryStore(),
		catalog: &fakePOSCatalog{
			prices: map[string]checkout.SKUPricing{
				"BP-CITY-BLK-20L":       {SkuID: "BP-CITY-BLK-20L", Name: "City Backpack 20L", UnitPriceVND: 300_000},
				"SKU-BOTTLE-BLUE-500ML": {SkuID: "SKU-BOTTLE-BLUE-500ML", Name: "Insulated Bottle 500ml", UnitPriceVND: 100_000},
			},
		},
		stock: &fakePOSStock{},
	}
	vouchers := voucher.NewMemoryStore(voucher.DevCampaigns()...)
	svc := checkout.New(checkout.Deps{
		Orders:      f.orders,
		Catalog:     f.catalog,
		Stock:       f.stock,
		Snapshots:   f.snaps,
		WarehouseID: "WH-HCM-01",
	})
	f.srv = NewServer(svc, voucher.NewService(vouchers), voucher.NewAdmin(vouchers))
	return f
}

func (f *posFixture) postSale(t *testing.T, token, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/pos/sales", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, req)
	return rec
}

func (f *posFixture) getSales(t *testing.T, token, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, req)
	return rec
}

type posReceiptEnvelope struct {
	Message string                  `json:"message"`
	Data    checkout.POSSaleReceipt `json:"data"`
}

type posListEnvelope struct {
	Message string                    `json:"message"`
	Data    []checkout.POSSaleSummary `json:"data"`
}

func TestPOSSale_HappyPathCashAndCard(t *testing.T) {
	f := newPOSFixture(t)
	hcmStaff := scopedBearer(t, "usr-staff-hcm", []string{"WAREHOUSE_STAFF"}, "WH-HCM-01")

	cashBody := `{
		"warehouseId": "WH-HCM-01",
		"paymentMethod": "POS_CASH",
		"customerNote": "Walk-in counter receipt",
		"cashReceivedVnd": 500000,
		"items": [
			{"skuId": "BP-CITY-BLK-20L", "quantity": 1}
		]
	}`
	rec := f.postSale(t, hcmStaff, "pos-key-cash-1", cashBody)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	got := decodeJSON[posReceiptEnvelope](t, rec).Data
	if got.Channel != "POS" || got.Status != "DELIVERY_COMPLETE" || got.PaymentStatus != "COLLECTED" ||
		got.PaymentMethod != "POS_CASH" || got.WarehouseID != "WH-HCM-01" || got.CashierID != "usr-staff-hcm" {
		t.Fatalf("unexpected receipt metadata: %+v", got)
	}
	if got.SubtotalVND != 300_000 || got.TotalVND != 300_000 || got.CashReceivedVND != 500_000 || got.ChangeDueVND != 200_000 {
		t.Fatalf("unexpected totals/change: %+v", got)
	}
	if !strings.HasPrefix(got.ReceiptNumber, "POS-HCM-") || !strings.HasPrefix(got.OrderID, "ord-pos-") {
		t.Fatalf("unexpected identifiers: orderId=%s receipt=%s", got.OrderID, got.ReceiptNumber)
	}
	if len(got.Items) != 1 || got.Items[0].Name != "City Backpack 20L" || got.Items[0].UnitPriceVND != 300_000 || got.Items[0].LineTotalVND != 300_000 {
		t.Fatalf("unexpected receipt items: %+v", got.Items)
	}
	if f.stock.reserves.Load() != 1 || f.stock.commits.Load() != 1 || f.stock.handovers.Load() != 1 {
		t.Fatalf("expected reserve=1 commit=1 handover=1, got %d/%d/%d", f.stock.reserves.Load(), f.stock.commits.Load(), f.stock.handovers.Load())
	}

	outboxEvents := f.orders.Outbox()
	if len(outboxEvents) != 1 || outboxEvents[0].EventType != events.EventPOSSaleCompleted {
		t.Fatalf("expected 1 pos.sale.completed outbox event, got %+v", outboxEvents)
	}
	snap, found, err := f.snaps.Get(context.Background(), got.OrderID)
	if err != nil || !found || snap.Channel != "POS" || snap.TotalPayableVND != 300_000 {
		t.Fatalf("expected POS snapshot, got found=%v err=%v snap=%+v", found, err, snap)
	}

	opsMgr := scopedBearer(t, "usr-ops-1", []string{"OPS_MGR"}, "ALL")
	cardBody := `{
		"warehouseId": "WH-HN-01",
		"paymentMethod": "POS_CARD",
		"items": [
			{"skuId": "BP-CITY-BLK-20L", "quantity": 1},
			{"skuId": "SKU-BOTTLE-BLUE-500ML", "quantity": 2}
		]
	}`
	cardRec := f.postSale(t, opsMgr, "pos-key-card-1", cardBody)
	if cardRec.Code != http.StatusCreated {
		t.Fatalf("expected 201 for card sale, got %d: %s", cardRec.Code, cardRec.Body.String())
	}
	cardGot := decodeJSON[posReceiptEnvelope](t, cardRec).Data
	if cardGot.PaymentMethod != "POS_CARD" || cardGot.TotalVND != 500_000 || cardGot.CashReceivedVND != 0 || cardGot.ChangeDueVND != 0 || !strings.HasPrefix(cardGot.ReceiptNumber, "POS-HN-") {
		t.Fatalf("unexpected card receipt: %+v", cardGot)
	}
}

func TestPOSSale_IdempotentReplayAndConflict(t *testing.T) {
	f := newPOSFixture(t)
	token := scopedBearer(t, "usr-staff-hcm", []string{"WAREHOUSE_STAFF"}, "WH-HCM-01")
	body := `{"warehouseId":"WH-HCM-01","paymentMethod":"POS_CASH","cashReceivedVnd":400000,"items":[{"skuId":"BP-CITY-BLK-20L","quantity":1}]}`

	first := f.postSale(t, token, "idem-pos-1", body)
	if first.Code != http.StatusCreated {
		t.Fatalf("first = %d %s", first.Code, first.Body.String())
	}
	firstReceipt := decodeJSON[posReceiptEnvelope](t, first).Data

	second := f.postSale(t, token, "idem-pos-1", body)
	if second.Code != http.StatusOK {
		t.Fatalf("replay = %d %s", second.Code, second.Body.String())
	}
	secondReceipt := decodeJSON[posReceiptEnvelope](t, second).Data
	if secondReceipt.OrderID != firstReceipt.OrderID || secondReceipt.ReceiptNumber != firstReceipt.ReceiptNumber {
		t.Fatalf("replay receipt mismatch: %+v vs %+v", secondReceipt, firstReceipt)
	}
	if f.stock.reserves.Load() != 1 || f.stock.commits.Load() != 1 || f.stock.handovers.Load() != 1 {
		t.Fatalf("inventory must only be called once on replay, got %d/%d/%d", f.stock.reserves.Load(), f.stock.commits.Load(), f.stock.handovers.Load())
	}
	if len(f.orders.Outbox()) != 1 {
		t.Fatalf("expected 1 outbox event after replay, got %d", len(f.orders.Outbox()))
	}

	diffBody := `{"warehouseId":"WH-HCM-01","paymentMethod":"POS_CARD","items":[{"skuId":"BP-CITY-BLK-20L","quantity":2}]}`
	conflict := f.postSale(t, token, "idem-pos-1", diffBody)
	if conflict.Code != http.StatusConflict || decodeJSON[orderError](t, conflict).Code != "IDEMPOTENCY_KEY_REUSED" {
		t.Fatalf("expected 409 IDEMPOTENCY_KEY_REUSED, got %d %s", conflict.Code, conflict.Body.String())
	}
}

func TestPOSSale_ValidationAndUnknownSKU(t *testing.T) {
	f := newPOSFixture(t)
	token := scopedBearer(t, "usr-staff-hcm", []string{"WAREHOUSE_STAFF"}, "WH-HCM-01")

	cases := []struct {
		name   string
		key    string
		body   string
		status int
		code   string
	}{
		{"missing idempotency key", "", `{"warehouseId":"WH-HCM-01","paymentMethod":"POS_CASH","items":[{"skuId":"BP-CITY-BLK-20L","quantity":1}]}`, http.StatusBadRequest, "MISSING_IDEMPOTENCY_KEY"},
		{"duplicate sku", "k-dup", `{"warehouseId":"WH-HCM-01","paymentMethod":"POS_CASH","items":[{"skuId":"BP-CITY-BLK-20L","quantity":1},{"skuId":"BP-CITY-BLK-20L","quantity":2}]}`, http.StatusBadRequest, "DUPLICATE_SKU"},
		{"insufficient cash tendered", "k-cash", `{"warehouseId":"WH-HCM-01","paymentMethod":"POS_CASH","cashReceivedVnd":100000,"items":[{"skuId":"BP-CITY-BLK-20L","quantity":1}]}`, http.StatusBadRequest, "INSUFFICIENT_CASH_TENDERED"},
		{"unknown sku", "k-sku", `{"warehouseId":"WH-HCM-01","paymentMethod":"POS_CARD","items":[{"skuId":"SKU-DOES-NOT-EXIST","quantity":1}]}`, http.StatusUnprocessableEntity, "INVALID_SKU"},
		{"empty items", "k-empty", `{"warehouseId":"WH-HCM-01","paymentMethod":"POS_CARD","items":[]}`, http.StatusBadRequest, "VALIDATION_FAILED"},
		{"invalid payment method", "k-pm", `{"warehouseId":"WH-HCM-01","paymentMethod":"COD","items":[{"skuId":"BP-CITY-BLK-20L","quantity":1}]}`, http.StatusBadRequest, "VALIDATION_FAILED"},
		{"client supplied price rejected", "k-price", `{"warehouseId":"WH-HCM-01","paymentMethod":"POS_CASH","unitPriceVnd":1,"items":[{"skuId":"BP-CITY-BLK-20L","quantity":1}]}`, http.StatusBadRequest, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := f.postSale(t, token, tc.key, tc.body)
			errResp := decodeJSON[orderError](t, rec)
			if rec.Code != tc.status || (tc.code != "" && errResp.Code != tc.code) {
				t.Fatalf("got %d %s, want %d %s", rec.Code, rec.Body.String(), tc.status, tc.code)
			}
		})
	}
	if f.stock.reserves.Load() != 0 {
		t.Fatalf("invalid POS requests must never touch inventory, got %d reserves", f.stock.reserves.Load())
	}
}

func TestPOSSale_InventoryInsufficientStockAndCompensation(t *testing.T) {
	t.Run("insufficient stock fails closed with 409", func(t *testing.T) {
		f := newPOSFixture(t)
		f.stock.reserveErr = checkout.ErrOutOfStock
		token := scopedBearer(t, "usr-staff-hcm", []string{"WAREHOUSE_STAFF"}, "WH-HCM-01")
		rec := f.postSale(t, token, "k-oos", `{"warehouseId":"WH-HCM-01","paymentMethod":"POS_CARD","items":[{"skuId":"BP-CITY-BLK-20L","quantity":99}]}`)
		if rec.Code != http.StatusConflict || decodeJSON[orderError](t, rec).Code != "INSUFFICIENT_STOCK" {
			t.Fatalf("got %d %s, want 409 INSUFFICIENT_STOCK", rec.Code, rec.Body.String())
		}
		if len(f.orders.Outbox()) != 0 {
			t.Fatalf("expected no outbox events on stock failure")
		}
		list, _, _ := f.orders.Query(context.Background(), orders.Filter{Channel: orders.ChannelPOS, Statuses: []string{orders.StatusDeliveryComplete}})
		if len(list) != 0 {
			t.Fatalf("expected no completed POS order on stock failure, got %+v", list)
		}
		failed, _, _ := f.orders.Query(context.Background(), orders.Filter{Channel: orders.ChannelPOS})
		if len(failed) != 1 || failed[0].Status != "FAILED_CLOSED" || failed[0].CapturedVND != 0 {
			t.Fatalf("expected FAILED_CLOSED POS intent with 0 capture, got %+v", failed)
		}
	})

	t.Run("commit failure compensates with release and returns 502", func(t *testing.T) {
		f := newPOSFixture(t)
		f.stock.commitErr = errors.New("commit failed")
		token := scopedBearer(t, "usr-staff-hcm", []string{"WAREHOUSE_STAFF"}, "WH-HCM-01")
		rec := f.postSale(t, token, "k-commit-fail", `{"warehouseId":"WH-HCM-01","paymentMethod":"POS_CARD","items":[{"skuId":"BP-CITY-BLK-20L","quantity":1}]}`)
		if rec.Code != http.StatusBadGateway || decodeJSON[orderError](t, rec).Code != "INVENTORY_UNAVAILABLE" {
			t.Fatalf("got %d %s, want 502 INVENTORY_UNAVAILABLE", rec.Code, rec.Body.String())
		}
		if f.stock.releases.Load() != 1 || f.stock.committedReleases.Load() != 0 {
			t.Fatalf("expected release=1 releaseCommitted=0, got %d/%d", f.stock.releases.Load(), f.stock.committedReleases.Load())
		}
	})

	t.Run("handover failure compensates with release-committed and returns 502", func(t *testing.T) {
		f := newPOSFixture(t)
		f.stock.handoverErr = errors.New("handover timeout")
		token := scopedBearer(t, "usr-staff-hcm", []string{"WAREHOUSE_STAFF"}, "WH-HCM-01")
		rec := f.postSale(t, token, "k-handover-fail", `{"warehouseId":"WH-HCM-01","paymentMethod":"POS_CARD","items":[{"skuId":"BP-CITY-BLK-20L","quantity":1}]}`)
		if rec.Code != http.StatusBadGateway || decodeJSON[orderError](t, rec).Code != "INVENTORY_UNAVAILABLE" {
			t.Fatalf("got %d %s, want 502 INVENTORY_UNAVAILABLE", rec.Code, rec.Body.String())
		}
		if f.stock.committedReleases.Load() != 1 {
			t.Fatalf("expected release-committed=1, got %d", f.stock.committedReleases.Load())
		}
		list, _, _ := f.orders.Query(context.Background(), orders.Filter{Channel: orders.ChannelPOS, Statuses: []string{orders.StatusDeliveryComplete}})
		if len(list) != 0 {
			t.Fatalf("expected no completed POS order on handover failure, got %+v", list)
		}
		if len(f.orders.Outbox()) != 0 {
			t.Fatalf("expected no outbox events on handover failure")
		}
	})
}

func TestPOSSale_ConcurrentDuplicateIdempotencyKeyDeductsStockOnce(t *testing.T) {
	f := newPOSFixture(t)
	token := scopedBearer(t, "usr-staff-hcm", []string{"WAREHOUSE_STAFF"}, "WH-HCM-01")
	body := `{"warehouseId":"WH-HCM-01","paymentMethod":"POS_CASH","cashReceivedVnd":400000,"items":[{"skuId":"BP-CITY-BLK-20L","quantity":1}]}`

	const callers = 10
	start := make(chan struct{})
	var wg sync.WaitGroup
	codes := make(chan int, callers)
	ids := make(chan string, callers)

	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			rec := f.postSale(t, token, "concurrent-pos-key", body)
			codes <- rec.Code
			if rec.Code == http.StatusCreated || rec.Code == http.StatusOK {
				ids <- decodeJSON[posReceiptEnvelope](t, rec).Data.OrderID
			}
		}()
	}
	close(start)
	wg.Wait()
	close(codes)
	close(ids)

	var createdCount, replayedCount int
	for c := range codes {
		switch c {
		case http.StatusCreated:
			createdCount++
		case http.StatusOK:
			replayedCount++
		default:
			t.Fatalf("unexpected status %d during concurrent duplicate POS checkout", c)
		}
	}
	if createdCount != 1 || replayedCount != callers-1 {
		t.Fatalf("expected 1 created and %d replayed, got created=%d replayed=%d", callers-1, createdCount, replayedCount)
	}
	var firstID string
	for id := range ids {
		if firstID == "" {
			firstID = id
		} else if id != firstID {
			t.Fatalf("divergent orderId %s vs %s", id, firstID)
		}
	}
	if f.stock.reserves.Load() != 1 || f.stock.commits.Load() != 1 || f.stock.handovers.Load() != 1 {
		t.Fatalf("inventory must execute once, got reserve=%d commit=%d handover=%d",
			f.stock.reserves.Load(), f.stock.commits.Load(), f.stock.handovers.Load())
	}
	if len(f.orders.Outbox()) != 1 {
		t.Fatalf("expected 1 outbox event, got %d", len(f.orders.Outbox()))
	}
}

func TestPOSSale_RBACAndWarehouseScopeAndListFiltering(t *testing.T) {
	f := newPOSFixture(t)
	hcmStaff := scopedBearer(t, "wh.hcm", []string{"WAREHOUSE_STAFF"}, "WH-HCM-01")
	hnMgr := scopedBearer(t, "wh.hn", []string{"WAREHOUSE_MGR"}, "WH-HN-01")
	opsMgr := scopedBearer(t, "ops.1", []string{"OPS_MGR"})
	customer := bearer(t, "cust-1", "CUSTOMER")
	marketing := bearer(t, "mkt-1", "MARKETING")

	bodyHCM := `{"warehouseId":"WH-HCM-01","paymentMethod":"POS_CASH","cashReceivedVnd":300000,"items":[{"skuId":"BP-CITY-BLK-20L","quantity":1}]}`
	bodyHN := `{"warehouseId":"WH-HN-01","paymentMethod":"POS_CARD","items":[{"skuId":"SKU-BOTTLE-BLUE-500ML","quantity":2}]}`

	if rec := f.postSale(t, "", "k-anon", bodyHCM); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous = %d, want 401", rec.Code)
	}
	for name, tok := range map[string]string{"customer": customer, "marketing": marketing} {
		if rec := f.postSale(t, tok, "k-"+name, bodyHCM); rec.Code != http.StatusForbidden || decodeJSON[orderError](t, rec).Code != "FORBIDDEN" {
			t.Fatalf("%s = %d %s, want 403 FORBIDDEN", name, rec.Code, rec.Body.String())
		}
	}

	if rec := f.postSale(t, hcmStaff, "k-cross-wh", bodyHN); rec.Code != http.StatusForbidden || decodeJSON[orderError](t, rec).Code != "WAREHOUSE_SCOPE_DENIED" {
		t.Fatalf("cross-warehouse = %d %s, want 403 WAREHOUSE_SCOPE_DENIED", rec.Code, rec.Body.String())
	}

	if rec := f.postSale(t, hcmStaff, "k-hcm-ok", bodyHCM); rec.Code != http.StatusCreated {
		t.Fatalf("hcm sale = %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.postSale(t, hnMgr, "k-hn-ok", bodyHN); rec.Code != http.StatusCreated {
		t.Fatalf("hn sale = %d %s", rec.Code, rec.Body.String())
	}
	_, _, _ = f.orders.Create(context.Background(), "online-k", "online-h", orders.Order{
		OrderID: "O-ONLINE-1", UserID: "cust-1", Channel: orders.ChannelOnline, WarehouseID: "WH-HCM-01",
		TotalPayableVND: 300_000, Items: []orders.Item{{SkuID: "BP-CITY-BLK-20L", Quantity: 1}},
	})

	var opsAll posListEnvelope
	rec := f.getSales(t, opsMgr, "/api/v1/admin/pos/sales")
	if rec.Code != http.StatusOK {
		t.Fatalf("ops list = %d %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &opsAll); err != nil {
		t.Fatal(err)
	}
	if len(opsAll.Data) != 2 {
		t.Fatalf("expected 2 POS sales (excluding online order), got %d", len(opsAll.Data))
	}

	recHcm := f.getSales(t, hcmStaff, "/api/v1/admin/pos/sales")
	hcmList := decodeJSON[posListEnvelope](t, recHcm).Data
	if recHcm.Code != http.StatusOK || len(hcmList) != 1 || hcmList[0].WarehouseID != "WH-HCM-01" || hcmList[0].ItemCount != 1 {
		t.Fatalf("hcm list = %d %+v", recHcm.Code, hcmList)
	}

	recDenied := f.getSales(t, hcmStaff, "/api/v1/admin/pos/sales?warehouseId=WH-HN-01")
	if recDenied.Code != http.StatusForbidden || decodeJSON[orderError](t, recDenied).Code != "WAREHOUSE_SCOPE_DENIED" {
		t.Fatalf("hcm querying WH-HN-01 = %d %s, want 403 WAREHOUSE_SCOPE_DENIED", recDenied.Code, recDenied.Body.String())
	}

	recLimit := f.getSales(t, opsMgr, "/api/v1/admin/pos/sales?warehouseId=WH-HN-01&limit=1")
	limitList := decodeJSON[posListEnvelope](t, recLimit).Data
	if recLimit.Code != http.StatusOK || len(limitList) != 1 || limitList[0].WarehouseID != "WH-HN-01" || limitList[0].ItemCount != 2 {
		t.Fatalf("filtered list = %d %+v", recLimit.Code, limitList)
	}
}
