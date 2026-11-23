package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/orders"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/snapshot"
)

func scopedBearer(t *testing.T, user string, roles []string, warehouses ...string) string {
	t.Helper()
	tok, err := auth.SignAccessToken(auth.Identity{UserID: user, Roles: roles, WarehouseScopes: warehouses})
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

type adminList struct {
	Data []struct {
		OrderID         string `json:"orderId"`
		CustomerID      string `json:"customerId"`
		Status          string `json:"status"`
		WarehouseID     string `json:"warehouseId"`
		TotalPayableVND int64  `json:"totalPayableVnd"`
		ItemCount       int64  `json:"itemCount"`
		Recipient       string `json:"recipient"`
		Phone           string `json:"phone"`
		District        string `json:"district"`
		Province        string `json:"province"`
	} `json:"data"`
	Metadata struct {
		Total int64 `json:"total"`
	} `json:"metadata"`
}

func TestMaskHidesTheNameAndThePhone(t *testing.T) {
	for in, want := range map[string]string{
		"Nguyen Van Lan": "N*** V*** L***",
		"Lan":            "L***",
		"":               "",
	} {
		if got := maskName(in); got != want {
			t.Errorf("maskName(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"0901234567": "*******567", "12": "**", "": ""} {
		if got := maskPhone(in); got != want {
			t.Errorf("maskPhone(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStaffOrderListMasksPersonalDataAndNeedsAStaffRole(t *testing.T) {
	f := newOrderFixture(t)
	id := f.placeOne(t)
	for _, role := range []string{"SUPPORT_AGENT", "SUPPORT_LEAD", "OPS_MGR"} {
		rec := f.get(t, scopedBearer(t, "staff-1", []string{role}), "/api/v1/admin/orders")
		got := decodeJSON[adminList](t, rec)
		if rec.Code != http.StatusOK || len(got.Data) != 1 || got.Data[0].OrderID != id || got.Metadata.Total != 1 {
			t.Fatalf("%s = %d %s", role, rec.Code, rec.Body.String())
		}
		row := got.Data[0]
		if row.Recipient != "N*** V*** L***" || row.Phone != "*******567" || row.District != "District 1" || row.Province != "Ho Chi Minh" ||
			row.CustomerID != "user-lan" || row.TotalPayableVND != 660000 || row.ItemCount != 3 || row.WarehouseID != "WH-HCM-01" {
			t.Fatalf("row = %+v", row)
		}
		for _, secret := range []string{"Nguyen", "Lan\"", "0901234567", "1 Le Loi", "Ben Nghe", "addr-1"} {
			if strings.Contains(rec.Body.String(), secret) {
				t.Fatalf("%s leaked %q: %s", role, secret, rec.Body.String())
			}
		}
	}
	for name, token := range map[string]string{
		"anonymous": "", "customer": bearer(t, "user-lan", "CUSTOMER"), "product staff": bearer(t, "p", "PRODUCT_STAFF"),
		"warehouse staff": bearer(t, "w", "WAREHOUSE_STAFF"), "access admin": bearer(t, "a", "ACCESS_ADMIN"),
	} {
		want := http.StatusForbidden
		if token == "" {
			want = http.StatusUnauthorized
		}
		if rec := f.get(t, token, "/api/v1/admin/orders"); rec.Code != want {
			t.Errorf("%s list = %d, want %d", name, rec.Code, want)
		}
		if rec := f.get(t, token, "/api/v1/admin/orders/"+id); rec.Code != want {
			t.Errorf("%s detail = %d, want %d", name, rec.Code, want)
		}
	}
}

func TestWarehouseManagerSeesOnlyTheirWarehouses(t *testing.T) {
	f := newOrderFixture(t)
	id := f.placeOne(t)
	hcm := scopedBearer(t, "wm-1", []string{"WAREHOUSE_MGR"}, "WH-HCM-01")
	hn := scopedBearer(t, "wm-2", []string{"WAREHOUSE_MGR"}, "WH-HN-01")
	all := scopedBearer(t, "wm-3", []string{"WAREHOUSE_MGR"}, "ALL")
	none := scopedBearer(t, "wm-4", []string{"WAREHOUSE_MGR"})

	if got := decodeJSON[adminList](t, f.get(t, hcm, "/api/v1/admin/orders")); len(got.Data) != 1 {
		t.Fatalf("own warehouse = %+v", got)
	}
	if got := decodeJSON[adminList](t, f.get(t, all, "/api/v1/admin/orders")); len(got.Data) != 1 {
		t.Fatalf("ALL = %+v", got)
	}
	for name, token := range map[string]string{"other warehouse": hn, "no scope": none} {
		if got := decodeJSON[adminList](t, f.get(t, token, "/api/v1/admin/orders")); len(got.Data) != 0 || got.Metadata.Total != 0 {
			t.Errorf("%s list = %+v", name, got)
		}
		if rec := f.get(t, token, "/api/v1/admin/orders/"+id); rec.Code != http.StatusForbidden {
			t.Errorf("%s detail = %d", name, rec.Code)
		}
	}
	if rec := f.get(t, hcm, "/api/v1/admin/orders/"+id); rec.Code != http.StatusOK {
		t.Errorf("own warehouse detail = %d", rec.Code)
	}
	// A manager who also holds a support role sees every order.
	both := scopedBearer(t, "wm-5", []string{"WAREHOUSE_MGR", "SUPPORT_AGENT"}, "WH-HN-01")
	if got := decodeJSON[adminList](t, f.get(t, both, "/api/v1/admin/orders")); len(got.Data) != 1 {
		t.Fatalf("manager with a support role = %+v", got)
	}
	// ?warehouse= narrows further and may not leave the scope.
	if rec := f.get(t, hcm, "/api/v1/admin/orders?warehouse=WH-HN-01"); rec.Code != http.StatusForbidden {
		t.Errorf("warehouse outside the scope = %d", rec.Code)
	}
}

func TestWarehouseManagerCanReadSplitOrderOnlyWithParcelScope(t *testing.T) {
	f := newOrderFixture(t)
	order := orders.Order{
		OrderID: "O-SPLIT-SCOPE", UserID: "buyer", WarehouseID: "MULTI",
		Parcels: []orders.Parcel{
			{ParcelSeq: 1, WarehouseID: "WH-HCM-01"},
			{ParcelSeq: 2, WarehouseID: "WH-HN-01"},
		},
	}
	if _, _, err := f.orders.Create(context.Background(), "split", "hash", order); err != nil {
		t.Fatal(err)
	}
	for warehouse, want := range map[string]int{"WH-HCM-01": 200, "WH-HN-01": 200, "WH-OUTSIDE": 403} {
		token := scopedBearer(t, "warehouse-manager", []string{"WAREHOUSE_MGR"}, warehouse)
		if got := f.get(t, token, "/api/v1/admin/orders/"+order.OrderID); got.Code != want {
			t.Errorf("warehouse %s: %d, want %d", warehouse, got.Code, want)
		}
	}
}

type adminDetail struct {
	Data struct {
		OrderID         string `json:"orderId"`
		CustomerID      string `json:"customerId"`
		Status          string `json:"status"`
		Recipient       string `json:"recipient"`
		Phone           string `json:"phone"`
		District        string `json:"district"`
		Province        string `json:"province"`
		VoucherCode     string `json:"voucherCode"`
		TotalPayableVND int64  `json:"totalPayableVnd"`
		Items           []struct {
			SkuID                string `json:"skuId"`
			AllocatedUnitPaidVND int64  `json:"allocatedUnitPaidVnd"`
		} `json:"items"`
		Payment struct {
			Status            string `json:"status"`
			CapturedAmountVND int64  `json:"capturedAmountVnd"`
		} `json:"payment"`
		RefundObligation *struct {
			AmountVND int64 `json:"amountVnd"`
		} `json:"refundObligation"`
	} `json:"data"`
}

func TestStaffOrderDetailHasItemsPaymentAndRefundButMaskedPeople(t *testing.T) {
	f := newOrderFixture(t)
	id := f.placeOne(t)
	f.outcome(t, id, outcomeBody(id, "UNKNOWN", 0), true)
	f.outcome(t, id, outcomeBody(id, "CAPTURED_LATE", 660000), true)
	rec := f.get(t, bearer(t, "staff-1", "SUPPORT_AGENT"), "/api/v1/admin/orders/"+id)
	got := decodeJSON[adminDetail](t, rec).Data
	if rec.Code != http.StatusOK || got.OrderID != id || got.Recipient != "N*** V*** L***" || got.Phone != "*******567" ||
		got.District != "District 1" || got.Province != "Ho Chi Minh" || got.VoucherCode != "WELCOME10" || got.CustomerID != "user-lan" {
		t.Fatalf("detail = %d %s", rec.Code, rec.Body.String())
	}
	if len(got.Items) != 2 || got.Items[0].AllocatedUnitPaidVND != 270000 || got.Payment.CapturedAmountVND != 660000 ||
		got.RefundObligation == nil || got.RefundObligation.AmountVND != 660000 {
		t.Fatalf("detail = %s", rec.Body.String())
	}
	for _, secret := range []string{"Nguyen", "0901234567", "1 Le Loi", "Ben Nghe"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatalf("detail leaked %q", secret)
		}
	}
	if rec := f.get(t, bearer(t, "staff-1", "SUPPORT_AGENT"), "/api/v1/admin/orders/O-nope"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown order = %d", rec.Code)
	}
}

func TestStaffOrderListPagesAndFiltersByStatus(t *testing.T) {
	f := newOrderFixture(t)
	for i := 0; i < 3; i++ {
		f.place(t, bearer(t, "user-lan", "CUSTOMER"), fmt.Sprintf("k-%d", i), fmt.Sprintf(`{"quoteId":"Q-%d"}`, i))
	}
	staff := bearer(t, "staff-1", "OPS_MGR")
	if got := decodeJSON[adminList](t, f.get(t, staff, "/api/v1/admin/orders?limit=2")); len(got.Data) != 2 || got.Metadata.Total != 3 {
		t.Fatalf("page = %+v", got)
	}
	if got := decodeJSON[adminList](t, f.get(t, staff, "/api/v1/admin/orders?status=CONFIRMED")); len(got.Data) != 0 {
		t.Fatalf("filter = %+v", got)
	}
	if rec := f.get(t, staff, "/api/v1/admin/orders?status=NOPE"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad status = %d", rec.Code)
	}
}

func TestContactOfPreservesRedactedAddressWithoutDoubleMasking(t *testing.T) {
	got := contactOf(snapshot.RedactedAddress())
	if got.Recipient != snapshot.RedactedField || got.Phone != snapshot.RedactedField || got.District != "" || got.Province != "" {
		t.Fatalf("contactOf(RedactedAddress()) = %+v, want recipient=%q phone=%q empty district/province", got, snapshot.RedactedField, snapshot.RedactedField)
	}
}
