package repository

import (
	"context"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/services/inventory-warehouse-service/internal/domain"
)

func TestProcurementRepoInMemory(t *testing.T) {
	repo := NewInventoryRepository(nil)
	ctx := context.Background()

	t.Run("suppliers CRUD", func(t *testing.T) {
		suppliers, err := repo.ListSuppliers(ctx)
		if err != nil {
			t.Fatalf("ListSuppliers failed: %v", err)
		}
		if len(suppliers) < 2 {
			t.Errorf("expected at least 2 seeded suppliers, got %d", len(suppliers))
		}

		created, err := repo.CreateSupplier(ctx, domain.Supplier{
			SupplierID: "SUP-TEST-01",
			Name:       "Test Supplier Ltd.",
			Code:       "TESTSUP",
		})
		if err != nil {
			t.Fatalf("CreateSupplier failed: %v", err)
		}
		if created.SupplierID != "SUP-TEST-01" {
			t.Errorf("got %s, want SUP-TEST-01", created.SupplierID)
		}

		s, ok, err := repo.GetSupplier(ctx, "SUP-TEST-01")
		if err != nil || !ok {
			t.Fatalf("GetSupplier failed: ok=%v, err=%v", ok, err)
		}
		if s.Name != "Test Supplier Ltd." {
			t.Errorf("got name %s, want Test Supplier Ltd.", s.Name)
		}
	})

	t.Run("create PO and approval lifecycle", func(t *testing.T) {
		po, err := repo.CreatePurchaseOrder(ctx, domain.PurchaseOrder{
			POID:        "PO-TEST-01",
			SupplierID:  "SUP-EVEREST-01",
			WarehouseID: "WH-HCM-01",
			CreatedBy:   "maker-user",
			Lines: []domain.PurchaseOrderLine{
				{SkuID: "SKU-BALO-BLACK-20L", OrderedQty: 50, UnitCostVND: 200_000},
			},
		})
		if err != nil {
			t.Fatalf("CreatePurchaseOrder failed: %v", err)
		}
		if po.TotalAmountVND != 10_000_000 {
			t.Errorf("expected 10M, got %d", po.TotalAmountVND)
		}
		if po.Status != domain.POStatusPendingApproval {
			t.Errorf("expected PENDING_APPROVAL, got %s", po.Status)
		}

		// Maker cannot self-approve (P10)
		_, err = repo.ApprovePurchaseOrder(ctx, "PO-TEST-01", "maker-user", []string{"PROCUREMENT_MGR"})
		if err != domain.ErrSelfApprovalRejected {
			t.Errorf("expected ErrSelfApprovalRejected, got %v", err)
		}

		// Checker approves
		approved, err := repo.ApprovePurchaseOrder(ctx, "PO-TEST-01", "checker-user", []string{"PROCUREMENT_MGR"})
		if err != nil {
			t.Fatalf("ApprovePurchaseOrder failed: %v", err)
		}
		if approved.Status != domain.POStatusApproved {
			t.Errorf("expected APPROVED, got %s", approved.Status)
		}
		if approved.ApprovedBy != "checker-user" {
			t.Errorf("expected checker-user, got %s", approved.ApprovedBy)
		}
	})
}
