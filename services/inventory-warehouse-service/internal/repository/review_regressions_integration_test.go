//go:build integration

package repository

import (
	"strings"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/services/inventory-warehouse-service/internal/domain"
)

func TestConcurrentReceivingAndSupplierReturnDispatch(t *testing.T) {
	pool := integrationPool(t)
	repo := NewInventoryRepository(pool)
	ctx := t.Context()
	first, second := seedStock(t, pool, 0), seedStock(t, pool, 0)
	po, err := repo.CreatePurchaseOrder(ctx, domain.PurchaseOrder{
		POID: uniqueID("IT-REVIEW-PO"), SupplierID: "SUP-EVEREST-01", CreatedBy: "buyer",
		Lines: []domain.PurchaseOrderLine{
			{SkuID: first, OrderedQty: 10, UnitCostVND: 100},
			{SkuID: second, OrderedQty: 1, UnitCostVND: 100},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ApprovePurchaseOrder(ctx, po.POID, "checker", []string{"PROCUREMENT_MGR"}); err != nil {
		t.Fatal(err)
	}
	receiveErrors := runConcurrently(2, func(_ int) error {
		_, err := repo.RecordReceivingInspection(ctx, po.POID, "inspector", "", []domain.ReceivingInspectionLine{
			{SkuID: first, DeliveredQty: 8, PassedQty: 4, DamagedQty: 4},
		})
		return err
	})
	requireOneSuccess(t, receiveErrors, "outstanding quantity")
	current, _, err := repo.GetPurchaseOrder(ctx, po.POID)
	if err != nil || current.Status != domain.POStatusPartiallyReceived {
		t.Fatalf("after partial receipt: %+v, %v", current, err)
	}
	if _, err := repo.RecordReceivingInspection(ctx, po.POID, "inspector", "", []domain.ReceivingInspectionLine{
		{SkuID: first, DeliveredQty: 2, PassedQty: 2},
		{SkuID: second, DeliveredQty: 1, PassedQty: 1},
	}); err != nil {
		t.Fatal(err)
	}
	current, _, err = repo.GetPurchaseOrder(ctx, po.POID)
	if err != nil || current.Status != domain.POStatusCompleted {
		t.Fatalf("after final receipt: %+v, %v", current, err)
	}
	ret, err := repo.CreateSupplierReturn(ctx, domain.SupplierReturn{
		ReturnID: uniqueID("IT-REVIEW-RTV"), SupplierID: po.SupplierID, POID: po.POID, CreatedBy: "inspector",
		Lines: []domain.SupplierReturnLine{{SkuID: first, Quantity: 2, Reason: "damaged"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	dispatchErrors := runConcurrently(2, func(_ int) error {
		_, err := repo.DispatchSupplierReturn(ctx, ret.ReturnID, "dispatcher", "TRACK-REVIEW")
		return err
	})
	requireOneSuccess(t, dispatchErrors, "not in DRAFT")
	got := stockOf(t, repo, first)
	if got.OnHand != 8 || got.Unsellable != 2 || got.Available() != 6 {
		t.Errorf("stock after one dispatch = %+v", got)
	}
	t.Logf("PO %s, supplier return %s, SKU %s", po.POID, ret.ReturnID, first)
}

func requireOneSuccess(t *testing.T, results []error, rejectedWith string) {
	t.Helper()
	successes := 0
	for _, err := range results {
		if err == nil {
			successes++
		} else if !strings.Contains(err.Error(), rejectedWith) {
			t.Fatalf("unexpected concurrent error: %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("got %d successes, want 1; results %v", successes, results)
	}
}

func TestReturnStockStepsAreAtomicAndIdempotent(t *testing.T) {
	pool := integrationPool(t)
	repo := NewInventoryRepository(pool)
	ctx := t.Context()
	sku := seedStock(t, pool, 0)
	key := uniqueID("IT-REVIEW-RMA")
	for _, err := range runConcurrently(8, func(_ int) error {
		_, err := repo.ReceiveReturnedItem(ctx, testWarehouse, sku, 2, key+":receive")
		return err
	}) {
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := repo.ReceiveReturnedItem(ctx, testWarehouse, sku, 1, key+":receive"); err != errReturnStepConflict {
		t.Fatalf("changed receive replay = %v", err)
	}
	if _, err := repo.ApproveReturnQC(ctx, testWarehouse, sku, 3, key+":pass"); err != domain.ErrInsufficientStock {
		t.Fatalf("oversized QC = %v", err)
	}
	for _, err := range runConcurrently(8, func(_ int) error {
		_, err := repo.ApproveReturnQC(ctx, testWarehouse, sku, 1, key+":pass")
		return err
	}) {
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := repo.ApproveReturnQC(ctx, testWarehouse, sku, 0, key+":pass"); err != errReturnStepConflict {
		t.Fatalf("changed QC replay = %v", err)
	}
	if _, err := repo.ApproveReturnQC(ctx, testWarehouse, sku, 0, key+":fail"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ApproveReturnQC(ctx, testWarehouse, sku, 1, key+":fail"); err != errReturnStepConflict {
		t.Fatalf("changed all-failed QC replay = %v", err)
	}
	got := stockOf(t, repo, sku)
	if got.OnHand != 2 || got.Unsellable != 1 || got.Available() != 1 {
		t.Errorf("stock after replays = %+v", got)
	}
	t.Logf("return step key prefix %s, SKU %s", key, sku)
}
