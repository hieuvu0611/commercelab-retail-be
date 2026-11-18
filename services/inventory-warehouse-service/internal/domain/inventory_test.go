package domain

import "testing"

func TestInventoryLifecycle_SC02AndSC07(t *testing.T) {
	engine := NewLedgerEngine([]StockItem{
		{SkuID: "SKU-BALO-BLACK-20L", WarehouseID: "WH-HCM-01", OnHand: 10, Unsellable: 0, Reserved: 0, Committed: 0},
	})

	lines := []ReserveLine{{SkuID: "SKU-BALO-BLACK-20L", Quantity: 2}}

	// 1. Giữ 2 chiếc cho O-001 -> Tồn thực tế 10, Giữ 2, Khả dụng 8
	if err := engine.ReserveAllOrNothing("WH-HCM-01", lines); err != nil {
		t.Fatalf("reserve failed: %v", err)
	}
	st, _ := engine.GetStock("WH-HCM-01", "SKU-BALO-BLACK-20L")
	if st.OnHand != 10 || st.Reserved != 2 || st.Available() != 8 {
		t.Fatalf("unexpected stock after reserve: %+v, available=%d", st, st.Available())
	}

	// 2. Chuyển giữ sang Cam kết -> Tồn thực tế 10, Giữ 0, Cam kết 2, Khả dụng vẫn là 8
	if err := engine.CommitReservation("WH-HCM-01", lines); err != nil {
		t.Fatalf("commit failed: %v", err)
	}
	st, _ = engine.GetStock("WH-HCM-01", "SKU-BALO-BLACK-20L")
	if st.OnHand != 10 || st.Reserved != 0 || st.Committed != 2 || st.Available() != 8 {
		t.Fatalf("unexpected stock after commit: %+v, available=%d", st, st.Available())
	}

	// 3. Bàn giao 2 chiếc -> Tồn thực tế giảm còn 8, Cam kết 0, Khả dụng vẫn là 8 (không trừ lần 2)
	if err := engine.HandoverShipment("WH-HCM-01", lines); err != nil {
		t.Fatalf("handover failed: %v", err)
	}
	st, _ = engine.GetStock("WH-HCM-01", "SKU-BALO-BLACK-20L")
	if st.OnHand != 8 || st.Committed != 0 || st.Available() != 8 {
		t.Fatalf("unexpected stock after handover: %+v, available=%d", st, st.Available())
	}

	// 4. SC07: Khách trả 1 chiếc về kho (chưa kiểm QC) -> Tồn thực tế 9, Không bán được 1, Khả dụng vẫn 8
	if err := engine.ReceiveReturnedItem("WH-HCM-01", "SKU-BALO-BLACK-20L", 1); err != nil {
		t.Fatalf("receive return failed: %v", err)
	}
	st, _ = engine.GetStock("WH-HCM-01", "SKU-BALO-BLACK-20L")
	if st.OnHand != 9 || st.Unsellable != 1 || st.Available() != 8 {
		t.Fatalf("unexpected stock after return receive: %+v, available=%d", st, st.Available())
	}

	// 5. SC07: Kiểm định đạt -> Không bán được về 0, Khả dụng tăng lên 9
	if err := engine.ApproveReturnQC("WH-HCM-01", "SKU-BALO-BLACK-20L", 1); err != nil {
		t.Fatalf("approve QC failed: %v", err)
	}
	st, _ = engine.GetStock("WH-HCM-01", "SKU-BALO-BLACK-20L")
	if st.OnHand != 9 || st.Unsellable != 0 || st.Available() != 9 {
		t.Fatalf("unexpected stock after QC pass: %+v, available=%d", st, st.Available())
	}
}

func TestSameSkuHasIndependentBucketsPerWarehouse(t *testing.T) {
	engine := NewLedgerEngine([]StockItem{
		{SkuID: "SKU-A", WarehouseID: "WH-HCM-01", OnHand: 5},
		{SkuID: "SKU-A", WarehouseID: "WH-HN-01", OnHand: 1},
	})
	if err := engine.ReserveAllOrNothing("WH-HN-01", []ReserveLine{{SkuID: "SKU-A", Quantity: 1}}); err != nil {
		t.Fatalf("reserve in HN: %v", err)
	}
	if err := engine.ReserveAllOrNothing("WH-HN-01", []ReserveLine{{SkuID: "SKU-A", Quantity: 1}}); err == nil {
		t.Fatal("HN has one unit only; the second reservation must fail")
	}
	hcm, _ := engine.GetStock("WH-HCM-01", "SKU-A")
	if hcm.Reserved != 0 || hcm.Available() != 5 {
		t.Fatalf("HCM must be untouched by an HN reservation: %+v", hcm)
	}
	if _, ok := engine.GetStock("WH-XX-01", "SKU-A"); ok {
		t.Fatal("a warehouse without stock must not resolve")
	}
}

func TestNormalizeLinesMergesSortsAndValidates(t *testing.T) {
	got, err := NormalizeLines([]ReserveLine{{SkuID: "B", Quantity: 1}, {SkuID: "A", Quantity: 2}, {SkuID: "B", Quantity: 3}})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	want := []ReserveLine{{SkuID: "A", Quantity: 2}, {SkuID: "B", Quantity: 4}}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for name, bad := range map[string][]ReserveLine{
		"empty":         nil,
		"blank sku":     {{SkuID: "", Quantity: 1}},
		"zero quantity": {{SkuID: "A", Quantity: 0}},
		"negative":      {{SkuID: "A", Quantity: -2}},
	} {
		if _, err := NormalizeLines(bad); err != ErrInvalidLines {
			t.Errorf("%s: err = %v, want ErrInvalidLines", name, err)
		}
	}
}

func TestDuplicateLinesAreCheckedAgainstTheirSum(t *testing.T) {
	engine := NewLedgerEngine([]StockItem{{SkuID: "SKU-A", WarehouseID: "WH-HCM-01", OnHand: 3}})
	lines, _ := NormalizeLines([]ReserveLine{{SkuID: "SKU-A", Quantity: 2}, {SkuID: "SKU-A", Quantity: 2}})
	if err := engine.ReserveAllOrNothing("WH-HCM-01", lines); err == nil {
		t.Fatal("2 + 2 > 3 must be rejected")
	}
}

func TestReceiveTransferInsufficientInTransitError(t *testing.T) {
	engine := NewLedgerEngine([]StockItem{{SkuID: "SKU-A", WarehouseID: "WH-HN-01", OnHand: 2, InTransit: 1}})
	err := engine.ReceiveTransfer("WH-HN-01", map[string]int64{"SKU-A": 3}, []TransferReceiveLine{
		{SkuID: "SKU-A", ReceivedGoodQty: 3},
	})
	if err != ErrTransferInsufficientInTransit {
		t.Fatalf("expected ErrTransferInsufficientInTransit, got %v", err)
	}
	errMissing := engine.ReceiveTransfer("WH-DN-01", map[string]int64{"SKU-A": 1}, []TransferReceiveLine{
		{SkuID: "SKU-A", ReceivedGoodQty: 1},
	})
	if errMissing != ErrTransferInsufficientInTransit {
		t.Fatalf("expected ErrTransferInsufficientInTransit for missing row, got %v", errMissing)
	}
}
