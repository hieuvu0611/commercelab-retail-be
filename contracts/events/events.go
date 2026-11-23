package events

import "time"

// DomainEvent là chuẩn sự kiện phát qua Transactional Outbox -> Kafka/Redpanda
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
	EventOrderFailed           = "order.failed.v1"
	EventOrderCancelRequested  = "order.cancel_requested.v1"
	EventOrderCancelled        = "order.cancelled.v1"
	EventOrderCancelRejected   = "order.cancel_rejected.v1"
	EventPaymentUnknown30m     = "payment.unknown_30m.v1"
	EventPaymentCaptured       = "payment.captured.v1"
	EventPaymentCODPending     = "payment.cod_pending.v1"
	EventPaymentCODCollected   = "payment.cod_collected.v1"
	EventPaymentCODCancelled   = "payment.cod_cancelled.v1"
	EventPaymentLateRefundNeed = "payment.late_refund_required.v1"
	EventPaymentRefunded       = "payment.refunded.v1"
	EventShipmentCreated       = "fulfillment.shipment_created.v1"
	EventShipmentHandedOver    = "fulfillment.shipment_handed_over.v1"
	EventShipmentDelivered     = "fulfillment.shipment_delivered.v1"
	EventShipmentStopRequested = "fulfillment.stop_requested.v1"
	EventShipmentStopped       = "fulfillment.shipment_stopped.v1"
	EventReturnApproved        = "support.return_approved.v1"
	EventReconDiscrepancyFound = "ledger.reconciliation_discrepancy.v1"
	EventExceptionCaseOpened   = "support.exception_case_opened.v1"
	EventUserPIIErased         = "iam.user_pii_erased.v1"
)
