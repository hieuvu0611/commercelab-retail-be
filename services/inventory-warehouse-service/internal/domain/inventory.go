package domain

import (
	"errors"
	"sort"
	"sync"
	"time"
)

// DefaultWarehouseID is the warehouse that read endpoints use when the caller names none.
// Writes never fall back to it: the warehouse must be explicit.
const DefaultWarehouseID = "WH-HCM-01"

var (
	ErrInsufficientStock       = errors.New("không đủ tồn kho khả dụng cho toàn bộ đơn (P03)")
	ErrInvalidReservationState = errors.New("trạng thái giữ hàng không hợp lệ")
	ErrInvalidLines            = errors.New("reservation lines are invalid: at least one line with a SKU and a positive quantity is required")
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

// Movement types of the stock ledger (I06): every change of a stock bucket has one record.
const (
	MovementOpening       = "OPENING"
	MovementReserve       = "RESERVE"
	MovementCommit        = "COMMIT"
	MovementRelease       = "RELEASE"
	MovementHandover      = "HANDOVER"
	MovementReturnReceive = "RETURN_RECEIVE"
	MovementQCPass        = "QC_PASS"
	MovementAdjust        = "ADJUST"
)

// SystemActor is the actor of movements that no signed-in user caused (order flow, workers).
const SystemActor = "system"

// Movement is one ledger record: who changed which bucket of which SKU in which warehouse, why and when.
type Movement struct {
	ID          int64     `json:"id"`
	SkuID       string    `json:"skuId"`
	WarehouseID string    `json:"warehouseId"`
	Type        string    `json:"type"`
	Qty         int64     `json:"qty"`
	RefType     string    `json:"refType,omitempty"`
	RefID       string    `json:"refId,omitempty"`
	ActorID     string    `json:"actorId"`
	Reason      string    `json:"reason"`
	At          time.Time `json:"at"`
}

// Availability states shown on the storefront instead of counts (decision D-12).
const (
	StateInStock = "IN_STOCK"
	StateLow     = "LOW"
	StateOut     = "OUT"
)

// DefaultLowStockThreshold is the largest available quantity that still counts as LOW.
const DefaultLowStockThreshold int64 = 3

// AvailabilityState maps an available quantity to its storefront state: OUT at 0 or below,
// LOW from 1 up to the threshold, IN_STOCK above it.
func AvailabilityState(available, lowThreshold int64) string {
	switch {
	case available <= 0:
		return StateOut
	case available <= lowThreshold:
		return StateLow
	default:
		return StateInStock
	}
}

type ReserveLine struct {
	SkuID    string `json:"skuId"`
	Quantity int64  `json:"quantity"`
}

// stockKey identifies one stock record: the same SKU has independent buckets in every warehouse.
type stockKey struct{ sku, warehouse string }

type LedgerEngine struct {
	mu     sync.Mutex
	stocks map[stockKey]*StockItem
}

// NormalizeLines merges lines of the same SKU, rejects empty lists, blank SKUs and quantities <= 0, and returns the
// lines sorted by SKU id. Locking rows in that order keeps two orders [A,B] and [B,A] from deadlocking.
func NormalizeLines(lines []ReserveLine) ([]ReserveLine, error) {
	if len(lines) == 0 {
		return nil, ErrInvalidLines
	}
	totals := make(map[string]int64, len(lines))
	for _, l := range lines {
		if l.SkuID == "" || l.Quantity <= 0 {
			return nil, ErrInvalidLines
		}
		totals[l.SkuID] += l.Quantity
	}
	out := make([]ReserveLine, 0, len(totals))
	for sku, qty := range totals {
		out = append(out, ReserveLine{SkuID: sku, Quantity: qty})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SkuID < out[j].SkuID })
	return out, nil
}

func NewLedgerEngine(initial []StockItem) *LedgerEngine {
	m := make(map[stockKey]*StockItem, len(initial))
	for _, it := range initial {
		cp := it
		m[stockKey{it.SkuID, it.WarehouseID}] = &cp
	}
	return &LedgerEngine{stocks: m}
}

func (e *LedgerEngine) GetStock(warehouseID, skuID string) (StockItem, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	it, ok := e.stocks[stockKey{skuID, warehouseID}]
	if !ok {
		return StockItem{}, false
	}
	return *it, true
}

// ReserveAllOrNothing thực thi P03 & SC03: Một kho phải đủ toàn bộ đơn, thiếu 1 dòng từ chối cả đơn
func (e *LedgerEngine) ReserveAllOrNothing(warehouseID string, lines []ReserveLine) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	for _, line := range lines {
		st, ok := e.stocks[stockKey{line.SkuID, warehouseID}]
		if !ok || line.Quantity <= 0 || st.Available() < line.Quantity {
			return ErrInsufficientStock
		}
	}
	for _, line := range lines {
		st := e.stocks[stockKey{line.SkuID, warehouseID}]
		st.Reserved += line.Quantity
		st.Version++
	}
	return nil
}

// CommitReservation chuyển Giữ tạm -> Cam kết chưa xuất khi thu tiền thành công (SC02: không đổi Khả dụng)
func (e *LedgerEngine) CommitReservation(warehouseID string, lines []ReserveLine) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	for _, line := range lines {
		st, ok := e.stocks[stockKey{line.SkuID, warehouseID}]
		if !ok || st.Reserved < line.Quantity {
			return ErrInvalidReservationState
		}
	}
	for _, line := range lines {
		st := e.stocks[stockKey{line.SkuID, warehouseID}]
		st.Reserved -= line.Quantity
		st.Committed += line.Quantity
		st.Version++
	}
	return nil
}

// ReleaseReservation nhả Giữ tạm về lại Khả dụng khi thanh toán thất bại hoặc hết hạn (SC05)
func (e *LedgerEngine) ReleaseReservation(warehouseID string, lines []ReserveLine) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	for _, line := range lines {
		st, ok := e.stocks[stockKey{line.SkuID, warehouseID}]
		if !ok || st.Reserved < line.Quantity {
			return ErrInvalidReservationState
		}
	}
	for _, line := range lines {
		st := e.stocks[stockKey{line.SkuID, warehouseID}]
		st.Reserved -= line.Quantity
		st.Version++
	}
	return nil
}

// HandoverShipment giảm đồng thời Tồn thực tế và Cam kết chưa xuất khi bàn giao bưu tá (SC02: không trừ Khả dụng lần 2)
func (e *LedgerEngine) HandoverShipment(warehouseID string, lines []ReserveLine) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	for _, line := range lines {
		st, ok := e.stocks[stockKey{line.SkuID, warehouseID}]
		if !ok || st.Committed < line.Quantity || st.OnHand < line.Quantity {
			return ErrInvalidReservationState
		}
	}
	for _, line := range lines {
		st := e.stocks[stockKey{line.SkuID, warehouseID}]
		st.OnHand -= line.Quantity
		st.Committed -= line.Quantity
		st.Version++
	}
	return nil
}

// ReceiveReturnedItem thực thi SC07: Kho nhận hàng trả -> tăng Tồn thực tế & Không bán được; Khả dụng giữ nguyên
func (e *LedgerEngine) ReceiveReturnedItem(warehouseID, skuID string, qty int64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	st, ok := e.stocks[stockKey{skuID, warehouseID}]
	if !ok || qty <= 0 {
		return ErrInvalidReservationState
	}
	st.OnHand += qty
	st.Unsellable += qty
	st.Version++
	return nil
}

// ApproveReturnQC thực thi SC07: Kiểm định đạt -> chuyển từ Không bán được sang Khả dụng
func (e *LedgerEngine) ApproveReturnQC(warehouseID, skuID string, qty int64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	st, ok := e.stocks[stockKey{skuID, warehouseID}]
	if !ok || qty <= 0 || st.Unsellable < qty {
		return ErrInvalidReservationState
	}
	st.Unsellable -= qty
	st.Version++
	return nil
}

// List returns the stock records of one warehouse sorted by SKU id.
func (e *LedgerEngine) List(warehouseID string) []StockItem {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]StockItem, 0)
	for k, it := range e.stocks {
		if k.warehouse == warehouseID {
			out = append(out, *it)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SkuID < out[j].SkuID })
	return out
}

// AddOpening adds opening stock to on hand (creating the record when needed).
func (e *LedgerEngine) AddOpening(warehouseID, skuID string, qty int64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if qty <= 0 {
		return ErrInvalidLines
	}
	k := stockKey{skuID, warehouseID}
	st, ok := e.stocks[k]
	if !ok {
		st = &StockItem{SkuID: skuID, WarehouseID: warehouseID, Version: 1}
		e.stocks[k] = st
	}
	st.OnHand += qty
	st.Version++
	return nil
}
