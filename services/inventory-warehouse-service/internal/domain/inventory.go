package domain

import (
	"errors"
	"sync"
)

var (
	ErrInsufficientStock       = errors.New("không đủ tồn kho khả dụng cho toàn bộ đơn (P03)")
	ErrInvalidReservationState = errors.New("trạng thái giữ hàng không hợp lệ")
)

// StockItem biểu diễn 5 rổ tồn kho theo B02, I01, SC02
type StockItem struct {
	SkuID       string `json:"skuId"`
	WarehouseID string `json:"warehouseId"`
	OnHand      int64  `json:"onHand"`     // Tồn thực tế
	Unsellable  int64  `json:"unsellable"` // Không bán được (hỏng, hàng trả chờ kiểm)
	Reserved    int64  `json:"reserved"`   // Giữ tạm chờ tiền (10p hoặc giữ ngoại lệ 30p)
	Committed   int64  `json:"committed"`  // Cam kết chưa xuất
	InTransit   int64  `json:"inTransit"`  // Đang chuyển kho (SC09)
	Version     int64  `json:"version"`
}

// Available tính tồn khả dụng theo công thức bất biến I01:
// Khả dụng = Tồn thực tế - Không bán được - Giữ tạm - Cam kết chưa xuất
func (s StockItem) Available() int64 {
	return s.OnHand - s.Unsellable - s.Reserved - s.Committed
}

type ReserveLine struct {
	SkuID    string `json:"skuId"`
	Quantity int64  `json:"quantity"`
}

type LedgerEngine struct {
	mu     sync.Mutex
	stocks map[string]*StockItem
}

func NewLedgerEngine(initial []StockItem) *LedgerEngine {
	m := make(map[string]*StockItem, len(initial))
	for _, it := range initial {
		cp := it
		m[it.SkuID] = &cp
	}
	return &LedgerEngine{stocks: m}
}

func (e *LedgerEngine) GetStock(skuID string) (StockItem, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	it, ok := e.stocks[skuID]
	if !ok {
		return StockItem{}, false
	}
	return *it, true
}

// ReserveAllOrNothing thực thi P03 & SC03: Một kho phải đủ toàn bộ đơn, thiếu 1 dòng từ chối cả đơn
func (e *LedgerEngine) ReserveAllOrNothing(lines []ReserveLine) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	for _, line := range lines {
		st, ok := e.stocks[line.SkuID]
		if !ok || line.Quantity <= 0 || st.Available() < line.Quantity {
			return ErrInsufficientStock
		}
	}
	for _, line := range lines {
		st := e.stocks[line.SkuID]
		st.Reserved += line.Quantity
		st.Version++
	}
	return nil
}

// CommitReservation chuyển Giữ tạm -> Cam kết chưa xuất khi thu tiền thành công (SC02: không đổi Khả dụng)
func (e *LedgerEngine) CommitReservation(lines []ReserveLine) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	for _, line := range lines {
		st, ok := e.stocks[line.SkuID]
		if !ok || st.Reserved < line.Quantity {
			return ErrInvalidReservationState
		}
	}
	for _, line := range lines {
		st := e.stocks[line.SkuID]
		st.Reserved -= line.Quantity
		st.Committed += line.Quantity
		st.Version++
	}
	return nil
}

// ReleaseReservation nhả Giữ tạm về lại Khả dụng khi thanh toán thất bại hoặc hết hạn (SC05)
func (e *LedgerEngine) ReleaseReservation(lines []ReserveLine) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	for _, line := range lines {
		st, ok := e.stocks[line.SkuID]
		if !ok || st.Reserved < line.Quantity {
			return ErrInvalidReservationState
		}
	}
	for _, line := range lines {
		st := e.stocks[line.SkuID]
		st.Reserved -= line.Quantity
		st.Version++
	}
	return nil
}

// HandoverShipment giảm đồng thời Tồn thực tế và Cam kết chưa xuất khi bàn giao bưu tá (SC02: không trừ Khả dụng lần 2)
func (e *LedgerEngine) HandoverShipment(lines []ReserveLine) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	for _, line := range lines {
		st, ok := e.stocks[line.SkuID]
		if !ok || st.Committed < line.Quantity || st.OnHand < line.Quantity {
			return ErrInvalidReservationState
		}
	}
	for _, line := range lines {
		st := e.stocks[line.SkuID]
		st.OnHand -= line.Quantity
		st.Committed -= line.Quantity
		st.Version++
	}
	return nil
}

// ReceiveReturnedItem thực thi SC07: Kho nhận hàng trả -> tăng Tồn thực tế & Không bán được; Khả dụng giữ nguyên
func (e *LedgerEngine) ReceiveReturnedItem(skuID string, qty int64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	st, ok := e.stocks[skuID]
	if !ok || qty <= 0 {
		return ErrInvalidReservationState
	}
	st.OnHand += qty
	st.Unsellable += qty
	st.Version++
	return nil
}

// ApproveReturnQC thực thi SC07: Kiểm định đạt -> chuyển từ Không bán được sang Khả dụng
func (e *LedgerEngine) ApproveReturnQC(skuID string, qty int64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	st, ok := e.stocks[skuID]
	if !ok || qty <= 0 || st.Unsellable < qty {
		return ErrInvalidReservationState
	}
	st.Unsellable -= qty
	st.Version++
	return nil
}
