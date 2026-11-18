package domain

import (
	"errors"
	"time"
)

var (
	ErrSupplierNotFound      = errors.New("supplier not found")
	ErrPurchaseOrderNotFound = errors.New("purchase order not found")
	ErrInvalidPOState        = errors.New("invalid purchase order state transition")
	ErrSelfApprovalRejected  = errors.New("maker-checker violation: PO creator cannot approve their own purchase order (P10)")
	ErrThresholdExceeded     = errors.New("PO amount exceeds approval authority for role")
	ErrInvalidPOLines        = errors.New("purchase order must contain at least one line with valid skuId, quantity > 0, and unitCostVnd >= 0")
)

// PO Statuses
const (
	POStatusDraft             = "DRAFT"
	POStatusPendingApproval   = "PENDING_APPROVAL"
	POStatusApproved          = "APPROVED"
	POStatusRejected          = "REJECTED"
	POStatusOrdered           = "ORDERED"
	POStatusPartiallyReceived = "PARTIALLY_RECEIVED"
	POStatusCompleted         = "COMPLETED"
	POStatusCancelled         = "CANCELLED"
)

// PO Approval Thresholds (B09, P10, Section 1.7)
// Up to 20,000,000 VND: PROCUREMENT_MGR, OPS_DIRECTOR, FINANCE_MGR, ADMIN
// Above 20,000,000 VND: OPS_DIRECTOR, FINANCE_MGR, ADMIN
const POProcurementManagerLimitVND int64 = 20_000_000

type Supplier struct {
	SupplierID   string    `json:"supplierId"`
	Name         string    `json:"name"`
	Code         string    `json:"code"`
	ContactEmail string    `json:"contactEmail"`
	Phone        string    `json:"phone"`
	Address      string    `json:"address"`
	Active       bool      `json:"active"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

type PurchaseOrderLine struct {
	ID          int64  `json:"id,omitempty"`
	POID        string `json:"poId,omitempty"`
	SkuID       string `json:"skuId"`
	OrderedQty  int64  `json:"orderedQty"`
	UnitCostVND int64  `json:"unitCostVnd"`
	ReceivedQty int64  `json:"receivedQty"`
	DamagedQty  int64  `json:"damagedQty"`
	ShortQty    int64  `json:"shortQty"`
}

type PurchaseOrder struct {
	POID           string              `json:"poId"`
	SupplierID     string              `json:"supplierId"`
	SupplierName   string              `json:"supplierName,omitempty"`
	WarehouseID    string              `json:"warehouseId"`
	Status         string              `json:"status"`
	TotalAmountVND int64               `json:"totalAmountVnd"`
	CreatedBy      string              `json:"createdBy"`
	ApprovedBy     string              `json:"approvedBy,omitempty"`
	ApprovedAt     *time.Time          `json:"approvedAt,omitempty"`
	Notes          string              `json:"notes"`
	Lines          []PurchaseOrderLine `json:"lines,omitempty"`
	CreatedAt      time.Time           `json:"createdAt"`
	UpdatedAt      time.Time           `json:"updatedAt"`
}

// ValidatePOLines validates lines and computes the total amount in VND.
func ValidatePOLines(lines []PurchaseOrderLine) (int64, error) {
	if len(lines) == 0 {
		return 0, ErrInvalidPOLines
	}
	var total int64
	for _, l := range lines {
		if l.SkuID == "" || l.OrderedQty <= 0 || l.UnitCostVND < 0 {
			return 0, ErrInvalidPOLines
		}
		total += l.OrderedQty * l.UnitCostVND
	}
	return total, nil
}

// CanApprovePO checks P10 (maker != checker) and role authorization based on the 20,000,000 VND threshold.
func CanApprovePO(po PurchaseOrder, approverID string, approverRoles []string) error {
	if approverID == "" {
		return errors.New("approver ID is required")
	}
	if po.CreatedBy == approverID {
		return ErrSelfApprovalRejected
	}
	if po.Status != POStatusPendingApproval {
		return ErrInvalidPOState
	}

	hasRole := func(target string) bool {
		for _, r := range approverRoles {
			if r == target {
				return true
			}
		}
		return false
	}

	isAdmin := hasRole("ADMIN")
	isDirector := hasRole("OPS_DIRECTOR") || hasRole("OPS_MGR") || hasRole("FINANCE_MGR")
	isProcMgr := hasRole("PROCUREMENT_MGR")

	if po.TotalAmountVND <= POProcurementManagerLimitVND {
		if isAdmin || isDirector || isProcMgr {
			return nil
		}
		return errors.New("PO approval requires at least PROCUREMENT_MGR role")
	}

	// Above 20M requires OPS_DIRECTOR, FINANCE_MGR or ADMIN
	if isAdmin || isDirector {
		return nil
	}
	return ErrThresholdExceeded
}

type ReceivingInspectionLine struct {
	ID           int64  `json:"id,omitempty"`
	InspectionID string `json:"inspectionId,omitempty"`
	SkuID        string `json:"skuId"`
	DeliveredQty int64  `json:"deliveredQty"`
	PassedQty    int64  `json:"passedQty"`
	DamagedQty   int64  `json:"damagedQty"`
	ShortQty     int64  `json:"shortQty"`
}

type ReceivingInspection struct {
	InspectionID string                    `json:"inspectionId"`
	POID         string                    `json:"poId"`
	WarehouseID  string                    `json:"warehouseId"`
	InspectorID  string                    `json:"inspectorId"`
	Status       string                    `json:"status"`
	Notes        string                    `json:"notes"`
	Lines        []ReceivingInspectionLine `json:"lines,omitempty"`
	CreatedAt    time.Time                 `json:"createdAt"`
}

// ValidateInspectionLine ensures SC09 consistency: delivered = passed + damaged, and quantities non-negative.
func ValidateInspectionLine(orderedQty, deliveredQty, passedQty, damagedQty int64) (shortQty int64, err error) {
	if deliveredQty < 0 || passedQty < 0 || damagedQty < 0 {
		return 0, errors.New("quantities must be non-negative")
	}
	if deliveredQty != (passedQty + damagedQty) {
		return 0, errors.New("deliveredQty must equal passedQty + damagedQty")
	}
	short := orderedQty - deliveredQty
	if short < 0 {
		short = 0
	}
	return short, nil
}

type SupplierReturnLine struct {
	ID       int64  `json:"id,omitempty"`
	ReturnID string `json:"returnId,omitempty"`
	SkuID    string `json:"skuId"`
	Quantity int64  `json:"quantity"`
	Reason   string `json:"reason"`
}

type SupplierReturn struct {
	ReturnID       string               `json:"returnId"`
	SupplierID     string               `json:"supplierId"`
	SupplierName   string               `json:"supplierName,omitempty"`
	WarehouseID    string               `json:"warehouseId"`
	POID           string               `json:"poId,omitempty"`
	Status         string               `json:"status"` // DRAFT, DISPATCHED, COMPLETED, CANCELLED
	CreatedBy      string               `json:"createdBy"`
	DispatchedBy   string               `json:"dispatchedBy,omitempty"`
	DispatchedAt   *time.Time           `json:"dispatchedAt,omitempty"`
	TrackingNumber string               `json:"trackingNumber,omitempty"`
	Notes          string               `json:"notes"`
	Lines          []SupplierReturnLine `json:"lines,omitempty"`
	CreatedAt      time.Time            `json:"createdAt"`
	UpdatedAt      time.Time            `json:"updatedAt"`
}

const (
	AdjustmentMaxQtyWarehouseMgr    int64 = 5
	AdjustmentMaxAmountWarehouseMgr int64 = 2_000_000
)

type StocktakeLine struct {
	ID            int64  `json:"id,omitempty"`
	StocktakeID   string `json:"stocktakeId,omitempty"`
	SkuID         string `json:"skuId"`
	SystemOnHand  int64  `json:"systemOnHand"`
	CountedOnHand int64  `json:"countedOnHand"`
	DiffQty       int64  `json:"diffQty"`
	UnitCostVND   int64  `json:"unitCostVnd"`
	DiffAmountVND int64  `json:"diffAmountVnd"`
}

type Stocktake struct {
	StocktakeID string          `json:"stocktakeId"`
	WarehouseID string          `json:"warehouseId"`
	Status      string          `json:"status"` // IN_PROGRESS, SUBMITTED, COMPLETED, CANCELLED
	StartedBy   string          `json:"startedBy"`
	Notes       string          `json:"notes"`
	Lines       []StocktakeLine `json:"lines,omitempty"`
	CreatedAt   time.Time       `json:"createdAt"`
	CompletedAt *time.Time      `json:"completedAt,omitempty"`
}

type StockAdjustment struct {
	AdjustmentID  string     `json:"adjustmentId"`
	StocktakeID   string     `json:"stocktakeId"`
	WarehouseID   string     `json:"warehouseId"`
	SkuID         string     `json:"skuId"`
	DiffQty       int64      `json:"diffQty"`
	UnitCostVND   int64      `json:"unitCostVnd"`
	DiffAmountVND int64      `json:"diffAmountVnd"`
	Status        string     `json:"status"` // PENDING_APPROVAL, APPROVED, REJECTED
	MakerID       string     `json:"makerId"`
	CheckerID     string     `json:"checkerId,omitempty"`
	DecidedAt     *time.Time `json:"decidedAt,omitempty"`
	Reason        string     `json:"reason"`
	CreatedAt     time.Time  `json:"createdAt"`
}

// CanApproveAdjustment verifies P10 maker != checker and threshold authority:
// Up to 5 units or 2,000,000 VND: WAREHOUSE_MGR, SUPPLY_CHAIN_MGR, ADMIN.
// Beyond that: SUPPLY_CHAIN_MGR, FINANCE_MGR, ADMIN.
func CanApproveAdjustment(adj StockAdjustment, approverID string, approverRoles []string) error {
	if approverID == "" {
		return errors.New("approver ID is required")
	}
	if adj.MakerID == approverID {
		return errors.New("maker-checker violation: requester cannot approve their own inventory adjustment (P10)")
	}
	if adj.Status != "PENDING_APPROVAL" {
		return errors.New("adjustment is not pending approval")
	}

	hasRole := func(target string) bool {
		for _, r := range approverRoles {
			if r == target {
				return true
			}
		}
		return false
	}

	isAdmin := hasRole("ADMIN")
	isSupplyChain := hasRole("SUPPLY_CHAIN_MGR") || hasRole("OPS_MGR") || hasRole("FINANCE_MGR")
	isWarehouseMgr := hasRole("WAREHOUSE_MGR")

	absQty := adj.DiffQty
	if absQty < 0 {
		absQty = -absQty
	}
	absAmount := adj.DiffAmountVND
	if absAmount < 0 {
		absAmount = -absAmount
	}

	if absQty <= AdjustmentMaxQtyWarehouseMgr && absAmount <= AdjustmentMaxAmountWarehouseMgr {
		if isAdmin || isSupplyChain || isWarehouseMgr {
			return nil
		}
		return errors.New("adjustment requires at least WAREHOUSE_MGR approval")
	}

	if isAdmin || isSupplyChain {
		return nil
	}
	return errors.New("adjustment exceeding 5 units or 2,000,000 VND requires Supply Chain or Finance Manager approval")
}
