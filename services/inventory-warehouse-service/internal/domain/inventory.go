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

// ActiveWarehouses lists the active 1P warehouses in Stage 3 (WH-HCM-01, WH-HN-01).
var ActiveWarehouses = []string{"WH-HCM-01", "WH-HN-01"}

// Movement types of the stock ledger (I06): every change of a stock bucket has one record.
const (
	MovementOpening           = "OPENING"
	MovementReserve           = "RESERVE"
	MovementCommit            = "COMMIT"
	MovementRelease           = "RELEASE"
	MovementHandover          = "HANDOVER"
	MovementReturnReceive     = "RETURN_RECEIVE"
	MovementQCPass            = "QC_PASS"
	MovementAdjust            = "ADJUST"
	MovementPOReceiveGood     = "PO_RECEIVE_GOOD"
	MovementPOReceiveDamage   = "PO_RECEIVE_DAMAGED"
	MovementSupplierReturn    = "SUPPLIER_RETURN"
	MovementTransferOut       = "TRANSFER_OUT"
	MovementTransferInGood    = "TRANSFER_IN_GOOD"
	MovementTransferInDamaged = "TRANSFER_IN_DAMAGED"
)

// WarehouseAllocation holds the lines reserved for one order in one warehouse.
type WarehouseAllocation struct {
	WarehouseID string        `json:"warehouseId"`
	Lines       []ReserveLine `json:"lines"`
}

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

// ReceiveInboundQC thực thi SC09: Tiếp nhận hàng và kiểm định chất lượng từ PO
// Nhận passedQty hàng đạt -> OnHand += passedQty, Available += passedQty
// Nhận damagedQty hàng hỏng -> OnHand += damagedQty, Unsellable += damagedQty, Available giữ nguyên
// Tổng cộng: OnHand += (passedQty + damagedQty), Unsellable += damagedQty, Available += passedQty
func (e *LedgerEngine) ReceiveInboundQC(warehouseID, skuID string, passedQty, damagedQty int64) error {
	if passedQty < 0 || damagedQty < 0 || (passedQty == 0 && damagedQty == 0) {
		return ErrInvalidLines
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	k := stockKey{skuID, warehouseID}
	st, ok := e.stocks[k]
	if !ok {
		st = &StockItem{SkuID: skuID, WarehouseID: warehouseID, Version: 1}
		e.stocks[k] = st
	}
	st.OnHand += (passedQty + damagedQty)
	st.Unsellable += damagedQty
	st.Version++
	return nil
}

// DispatchSupplierReturn giảm Tồn thực tế và Hàng không bán được khi xuất trả nhà cung cấp (I01: Khả dụng không đổi).
func (e *LedgerEngine) DispatchSupplierReturn(warehouseID, skuID string, qty int64) error {
	if qty <= 0 {
		return ErrInvalidLines
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	k := stockKey{skuID, warehouseID}
	st, ok := e.stocks[k]
	if !ok || st.OnHand < qty || st.Unsellable < qty {
		return ErrInsufficientStock
	}
	st.OnHand -= qty
	st.Unsellable -= qty
	st.Version++
	return nil
}

// QuarantineDiscrepancy khóa bán phần thiếu hụt khi kiểm kê kho (I01: giảm Khả dụng bằng cách tăng Unsellable).
func (e *LedgerEngine) QuarantineDiscrepancy(warehouseID, skuID string, shortageQty int64) error {
	if shortageQty <= 0 {
		return ErrInvalidLines
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	k := stockKey{skuID, warehouseID}
	st, ok := e.stocks[k]
	if !ok {
		return ErrInvalidReservationState
	}
	st.Unsellable += shortageQty
	st.Version++
	return nil
}

// ReleaseDiscrepancy makes the quarantined units available after a rejected adjustment.
func (e *LedgerEngine) ReleaseDiscrepancy(warehouseID, skuID string, shortageQty int64) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	st, ok := e.stocks[stockKey{skuID, warehouseID}]
	if !ok || shortageQty <= 0 {
		return ErrInvalidReservationState
	}
	st.Unsellable = max(0, st.Unsellable-shortageQty)
	st.Version++
	return nil
}

// ApplyAdjustment cập nhật tồn thực tế khi phiếu điều chỉnh được duyệt (P10).
func (e *LedgerEngine) ApplyAdjustment(warehouseID, skuID string, diffQty int64, shortageQuarantined bool) error {
	if diffQty == 0 {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	k := stockKey{skuID, warehouseID}
	st, ok := e.stocks[k]
	if !ok {
		return ErrInvalidReservationState
	}
	if diffQty < 0 {
		absShort := -diffQty
		if st.OnHand < absShort {
			return ErrInvalidReservationState
		}
		st.OnHand -= absShort
		if shortageQuarantined && st.Unsellable >= absShort {
			st.Unsellable -= absShort
		}
	} else {
		st.OnHand += diffQty
	}
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

// ReserveAllocations reserves stock across 1 or 2 warehouses atomically.
func (e *LedgerEngine) ReserveAllocations(allocations []WarehouseAllocation) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	for _, alloc := range allocations {
		for _, line := range alloc.Lines {
			st, ok := e.stocks[stockKey{line.SkuID, alloc.WarehouseID}]
			if !ok || line.Quantity <= 0 || st.Available() < line.Quantity {
				return ErrInsufficientStock
			}
		}
	}
	for _, alloc := range allocations {
		for _, line := range alloc.Lines {
			st := e.stocks[stockKey{line.SkuID, alloc.WarehouseID}]
			st.Reserved += line.Quantity
			st.Version++
		}
	}
	return nil
}

// DispatchTransfer moves qty from source OnHand into destination InTransit (SC09).
func (e *LedgerEngine) DispatchTransfer(fromWH, toWH string, lines []ReserveLine) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	for _, line := range lines {
		st, ok := e.stocks[stockKey{line.SkuID, fromWH}]
		if !ok || line.Quantity <= 0 || st.Available() < line.Quantity {
			return ErrInsufficientStock
		}
	}
	for _, line := range lines {
		src := e.stocks[stockKey{line.SkuID, fromWH}]
		src.OnHand -= line.Quantity
		src.Version++

		dstKey := stockKey{line.SkuID, toWH}
		dst, ok := e.stocks[dstKey]
		if !ok {
			dst = &StockItem{SkuID: line.SkuID, WarehouseID: toWH, Version: 1}
			e.stocks[dstKey] = dst
		}
		dst.InTransit += line.Quantity
		dst.Version++
	}
	return nil
}

// ReceiveTransfer clears InTransit on destination and records good/damaged quantities (SC09).
func (e *LedgerEngine) ReceiveTransfer(toWH string, dispatched map[string]int64, lines []TransferReceiveLine) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	for _, line := range lines {
		qty := dispatched[line.SkuID]
		if st, ok := e.stocks[stockKey{line.SkuID, toWH}]; !ok || st.InTransit < qty {
			return ErrTransferInsufficientInTransit
		}
	}
	for _, line := range lines {
		qty := dispatched[line.SkuID]
		dst := e.stocks[stockKey{line.SkuID, toWH}]
		dst.InTransit -= qty
		dst.OnHand += (line.ReceivedGoodQty + line.DamagedQty)
		dst.Unsellable += line.DamagedQty
		dst.Version++
	}
	return nil
}

// AvailableTotal returns combined sellable stock across all warehouses for one SKU.
func (e *LedgerEngine) AvailableTotal(skuID string) int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	var total int64
	for k, st := range e.stocks {
		if k.sku == skuID {
			if avail := st.Available(); avail > 0 {
				total += avail
			}
		}
	}
	return total
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

// ReleaseCommitted gives committed (paid, not yet handed over) stock back to available when a paid order is
// cancelled before handover. On hand does not change; committed shrinks, so available grows (I01).
func (e *LedgerEngine) ReleaseCommitted(warehouseID string, lines []ReserveLine) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	for _, line := range lines {
		st, ok := e.stocks[stockKey{line.SkuID, warehouseID}]
		if !ok || st.Committed < line.Quantity {
			return ErrInvalidReservationState
		}
	}
	for _, line := range lines {
		st := e.stocks[stockKey{line.SkuID, warehouseID}]
		st.Committed -= line.Quantity
		st.Version++
	}
	return nil
}
