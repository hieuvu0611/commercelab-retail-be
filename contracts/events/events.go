package events

import "time"

// DomainEvent represents a standard domain event published via Transactional Outbox / Event Bus.
type DomainEvent struct {
	EventID       string         `json:"eventId"`
	EventType     string         `json:"eventType"`
	AggregateType string         `json:"aggregateType"`
	AggregateID   string         `json:"aggregateId"`
	OccurredAt    time.Time      `json:"occurredAt"`
	Payload       map[string]any `json:"payload"`
}

const (
	EventOrderCreated          = "order.created.v1"
	EventOrderConfirmed        = "order.confirmed.v1"
	EventOrderCancelRequested  = "order.cancel_requested.v1"
	EventOrderCancelled        = "order.cancelled.v1"
	EventPaymentUnknown30m     = "payment.unknown_30m.v1"
	EventPaymentCaptured       = "payment.captured.v1"
	EventPaymentFailed         = "payment.failed.v1"
	EventPaymentLateRefundNeed = "payment.late_refund_required.v1"
	EventShipmentHandedOver    = "fulfillment.shipment_handed_over.v1"
	EventShipmentDelivered     = "fulfillment.shipment_delivered.v1"
	EventReturnApproved        = "support.return_approved.v1"
	EventReconDiscrepancyFound = "ledger.reconciliation_discrepancy.v1"
	EventExceptionCaseOpened   = "support.exception_case_opened.v1"
)
