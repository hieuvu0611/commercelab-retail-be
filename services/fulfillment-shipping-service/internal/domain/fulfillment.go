package domain

import (
	"errors"
	"sync"
)

var (
	ErrAlreadyHandedOver = errors.New("kiện hàng đã bàn giao cho bưu tá trước khi lệnh dừng có hiệu lực (SC06 Nhánh B)")
	ErrAlreadyStopped    = errors.New("kiện hàng đã bị dừng theo lệnh hủy đơn, không được phép bàn giao (SC06 Nhánh A)")
)

type Shipment struct {
	ShipmentID  string `json:"shipmentId"`
	OrderID     string `json:"orderId"`
	WarehouseID string `json:"warehouseId"`
	Status      string `json:"status"` // PICKING, PACKING, STOPPED_FOR_CANCEL, HANDED_OVER, DELIVERED
	Version     int64  `json:"version"`
}

type FulfillmentArbitrator struct {
	mu        sync.Mutex
	shipments map[string]*Shipment
}

func NewFulfillmentArbitrator() *FulfillmentArbitrator {
	return &FulfillmentArbitrator{
		shipments: map[string]*Shipment{
			"S-001": {ShipmentID: "S-001", OrderID: "O-001", WarehouseID: "WH-HCM-01", Status: "PACKING", Version: 1},
		},
	}
}

// RequestStopForCancel phân xử lệnh dừng kho khi khách yêu cầu hủy đơn (SC06)
func (a *FulfillmentArbitrator) RequestStopForCancel(shipmentID string) (*Shipment, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	s, ok := a.shipments[shipmentID]
	if !ok {
		return nil, errors.New("không tìm thấy kiện hàng")
	}
	if s.Status == "HANDED_OVER" || s.Status == "DELIVERED" {
		return nil, ErrAlreadyHandedOver
	}
	s.Status = "STOPPED_FOR_CANCEL"
	s.Version++
	cp := *s
	return &cp, nil
}

// ConfirmHandover phân xử quét bàn giao bưu tá (SC06)
func (a *FulfillmentArbitrator) ConfirmHandover(shipmentID string) (*Shipment, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	s, ok := a.shipments[shipmentID]
	if !ok {
		return nil, errors.New("không tìm thấy kiện hàng")
	}
	if s.Status == "STOPPED_FOR_CANCEL" {
		return nil, ErrAlreadyStopped
	}
	s.Status = "HANDED_OVER"
	s.Version++
	cp := *s
	return &cp, nil
}
