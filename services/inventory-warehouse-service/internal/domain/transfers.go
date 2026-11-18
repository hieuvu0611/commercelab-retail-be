package domain

import (
	"errors"
	"time"
)

const (
	TransferStatusDraft                   = "DRAFT"
	TransferStatusInTransit               = "IN_TRANSIT"
	TransferStatusReceived                = "RECEIVED"
	TransferStatusReceivedWithDiscrepancy = "RECEIVED_WITH_DISCREPANCY"
	TransferStatusCancelled               = "CANCELLED"
)

var (
	ErrTransferNotFound      = errors.New("warehouse transfer not found")
	ErrTransferInvalidState  = errors.New("warehouse transfer is not in a valid state for this action")
	ErrTransferInvalidInput  = errors.New("invalid warehouse transfer input")
	ErrTransferReceiveMath   = errors.New("received good + damaged + missing must equal dispatched quantity")
	ErrTransferInsufficientInTransit = errors.New("insufficient in-transit stock or missing destination stock row")
)

type TransferItem struct {
	SkuID           string `json:"skuId"`
	Quantity        int64  `json:"quantity"`
	ReceivedGoodQty int64  `json:"receivedGoodQty"`
	DamagedQty      int64  `json:"damagedQty"`
	MissingQty      int64  `json:"missingQty"`
}

type TransferReceiveLine struct {
	SkuID           string `json:"skuId"`
	ReceivedGoodQty int64  `json:"receivedGoodQty"`
	DamagedQty      int64  `json:"damagedQty"`
	MissingQty      int64  `json:"missingQty"`
}

type WarehouseTransfer struct {
	TransferID      string         `json:"transferId"`
	FromWarehouseID string         `json:"fromWarehouseId"`
	ToWarehouseID   string         `json:"toWarehouseId"`
	Status          string         `json:"status"`
	CreatedBy       string         `json:"createdBy"`
	DispatchedBy    string         `json:"dispatchedBy,omitempty"`
	ReceivedBy      string         `json:"receivedBy,omitempty"`
	ExceptionCaseID string         `json:"exceptionCaseId,omitempty"`
	Notes           string         `json:"notes,omitempty"`
	Items           []TransferItem `json:"items"`
	DispatchedAt    *time.Time     `json:"dispatchedAt,omitempty"`
	ReceivedAt      *time.Time     `json:"receivedAt,omitempty"`
	CreatedAt       time.Time      `json:"createdAt"`
	UpdatedAt       time.Time      `json:"updatedAt"`
}
