package domain

import (
	"testing"
)

func TestValidatePOLines(t *testing.T) {
	t.Run("valid lines compute total correctly", func(t *testing.T) {
		lines := []PurchaseOrderLine{
			{SkuID: "SKU-BACKPACK-BLK", OrderedQty: 100, UnitCostVND: 200_000},
			{SkuID: "SKU-BOTTLE-BLU", OrderedQty: 50, UnitCostVND: 100_000},
		}
		total, err := ValidatePOLines(lines)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		expected := int64(100*200_000 + 50*100_000)
		if total != expected {
			t.Errorf("got %d, want %d", total, expected)
		}
	})

	t.Run("empty lines rejected", func(t *testing.T) {
		_, err := ValidatePOLines(nil)
		if err != ErrInvalidPOLines {
			t.Errorf("got %v, want %v", err, ErrInvalidPOLines)
		}
	})

	t.Run("zero or negative quantity rejected", func(t *testing.T) {
		lines := []PurchaseOrderLine{
			{SkuID: "SKU-1", OrderedQty: 0, UnitCostVND: 10_000},
		}
		if _, err := ValidatePOLines(lines); err != ErrInvalidPOLines {
			t.Errorf("got %v, want %v", err, ErrInvalidPOLines)
		}
	})

	t.Run("blank sku rejected", func(t *testing.T) {
		lines := []PurchaseOrderLine{
			{SkuID: "", OrderedQty: 10, UnitCostVND: 10_000},
		}
		if _, err := ValidatePOLines(lines); err != ErrInvalidPOLines {
			t.Errorf("got %v, want %v", err, ErrInvalidPOLines)
		}
	})
}

func TestCanApprovePO_P10_Thresholds(t *testing.T) {
	poBase := PurchaseOrder{
		POID:           "PO-001",
		CreatedBy:      "user-maker-01",
		Status:         POStatusPendingApproval,
		TotalAmountVND: 15_000_000,
	}

	t.Run("self approval rejected by P10 maker != checker", func(t *testing.T) {
		err := CanApprovePO(poBase, "user-maker-01", []string{"PROCUREMENT_MGR", "ADMIN"})
		if err != ErrSelfApprovalRejected {
			t.Errorf("expected ErrSelfApprovalRejected, got %v", err)
		}
	})

	t.Run("not pending approval rejected", func(t *testing.T) {
		poDraft := poBase
		poDraft.Status = POStatusDraft
		err := CanApprovePO(poDraft, "checker-01", []string{"ADMIN"})
		if err != ErrInvalidPOState {
			t.Errorf("expected ErrInvalidPOState, got %v", err)
		}
	})

	t.Run("under 20M approved by PROCUREMENT_MGR", func(t *testing.T) {
		err := CanApprovePO(poBase, "checker-01", []string{"PROCUREMENT_MGR"})
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("under 20M approved by ADMIN", func(t *testing.T) {
		err := CanApprovePO(poBase, "checker-01", []string{"ADMIN"})
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("under 20M rejected if role is only WAREHOUSE_STAFF", func(t *testing.T) {
		err := CanApprovePO(poBase, "checker-01", []string{"WAREHOUSE_STAFF"})
		if err == nil {
			t.Error("expected error for insufficient role, got nil")
		}
	})

	t.Run("above 20M rejected for PROCUREMENT_MGR", func(t *testing.T) {
		poHigh := poBase
		poHigh.TotalAmountVND = 25_000_000
		err := CanApprovePO(poHigh, "checker-01", []string{"PROCUREMENT_MGR"})
		if err != ErrThresholdExceeded {
			t.Errorf("expected ErrThresholdExceeded, got %v", err)
		}
	})

	t.Run("above 20M approved by OPS_DIRECTOR", func(t *testing.T) {
		poHigh := poBase
		poHigh.TotalAmountVND = 25_000_000
		err := CanApprovePO(poHigh, "checker-01", []string{"OPS_DIRECTOR"})
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("above 20M approved by FINANCE_MGR", func(t *testing.T) {
		poHigh := poBase
		poHigh.TotalAmountVND = 50_000_000
		err := CanApprovePO(poHigh, "checker-01", []string{"FINANCE_MGR"})
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("high-impact PO and adjustment approved by OPS_MGR", func(t *testing.T) {
		poHigh := poBase
		poHigh.TotalAmountVND = 50_000_000
		if err := CanApprovePO(poHigh, "checker-01", []string{"OPS_MGR"}); err != nil {
			t.Errorf("unexpected PO error for OPS_MGR: %v", err)
		}
		adj := StockAdjustment{
			MakerID:       "maker-01",
			Status:        "PENDING_APPROVAL",
			DiffQty:       -10,
			DiffAmountVND: 3_000_000,
		}
		if err := CanApproveAdjustment(adj, "checker-01", []string{"OPS_MGR"}); err != nil {
			t.Errorf("unexpected adjustment error for OPS_MGR: %v", err)
		}
	})
}
