package domain

import "testing"

func TestInventoryLifecycle_SC02AndSC07(t *testing.T) {
	engine := NewLedgerEngine([]StockItem{
		{SkuID: "SKU-BALO-BLACK-20L", WarehouseID: "WH-HCM-01", OnHand: 10, Unsellable: 0, Reserved: 0, Committed: 0},
	})

	lines := []ReserveLine{{SkuID: "SKU-BALO-BLACK-20L", Quantity: 2}}

	// 1. Giữ 2 chiếc cho O-001 -> Tồn thực tế 10, Giữ 2, Khả dụng 8
	if err := engine.ReserveAllOrNothing(lines); err != nil {
		t.Fatalf("reserve failed: %v", err)
	}
	st, _ := engine.GetStock("SKU-BALO-BLACK-20L")
	if st.OnHand != 10 || st.Reserved != 2 || st.Available() != 8 {
		t.Fatalf("unexpected stock after reserve: %+v, available=%d", st, st.Available())
	}

	// 2. Chuyển giữ sang Cam kết -> Tồn thực tế 10, Giữ 0, Cam kết 2, Khả dụng vẫn là 8
	if err := engine.CommitReservation(lines); err != nil {
		t.Fatalf("commit failed: %v", err)
	}
	st, _ = engine.GetStock("SKU-BALO-BLACK-20L")
	if st.OnHand != 10 || st.Reserved != 0 || st.Committed != 2 || st.Available() != 8 {
		t.Fatalf("unexpected stock after commit: %+v, available=%d", st, st.Available())
	}

	// 3. Bàn giao 2 chiếc -> Tồn thực tế giảm còn 8, Cam kết 0, Khả dụng vẫn là 8 (không trừ lần 2)
	if err := engine.HandoverShipment(lines); err != nil {
		t.Fatalf("handover failed: %v", err)
	}
	st, _ = engine.GetStock("SKU-BALO-BLACK-20L")
	if st.OnHand != 8 || st.Committed != 0 || st.Available() != 8 {
		t.Fatalf("unexpected stock after handover: %+v, available=%d", st, st.Available())
	}

	// 4. SC07: Khách trả 1 chiếc về kho (chưa kiểm QC) -> Tồn thực tế 9, Không bán được 1, Khả dụng vẫn 8
	if err := engine.ReceiveReturnedItem("SKU-BALO-BLACK-20L", 1); err != nil {
		t.Fatalf("receive return failed: %v", err)
	}
	st, _ = engine.GetStock("SKU-BALO-BLACK-20L")
	if st.OnHand != 9 || st.Unsellable != 1 || st.Available() != 8 {
		t.Fatalf("unexpected stock after return receive: %+v, available=%d", st, st.Available())
	}

	// 5. SC07: Kiểm định đạt -> Không bán được về 0, Khả dụng tăng lên 9
	if err := engine.ApproveReturnQC("SKU-BALO-BLACK-20L", 1); err != nil {
		t.Fatalf("approve QC failed: %v", err)
	}
	st, _ = engine.GetStock("SKU-BALO-BLACK-20L")
	if st.OnHand != 9 || st.Unsellable != 0 || st.Available() != 9 {
		t.Fatalf("unexpected stock after QC pass: %+v, available=%d", st, st.Available())
	}
}
