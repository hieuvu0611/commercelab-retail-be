package domain

import "time"

// ReconciliationCase represents an open financial discrepancy requiring investigation (SC11)
type ReconciliationCase struct {
	CaseID            string     `json:"caseId"`
	BatchID           string     `json:"batchId"`
	LineNumber        int        `json:"lineNumber"`
	OrderID           string     `json:"orderId"`
	TransactionRef    string     `json:"transactionRef"`
	ExpectedPayoutVND int64      `json:"expectedPayoutVnd"`
	ActualPayoutVND   int64      `json:"actualPayoutVnd"`
	DiscrepancyVND    int64      `json:"discrepancyVnd"`
	CauseCategory     string     `json:"causeCategory"` // UNCLASSIFIED, FEE_ADJUSTMENT, BANK_CHARGE, GATEWAY_DISPUTE, OTHER
	Status            string     `json:"status"`        // OPEN, RESOLVED, ESCALATED, CLOSED
	AssignedTo        string     `json:"assignedTo"`
	ResolvedBy        string     `json:"resolvedBy"`
	ResolutionNotes   string     `json:"resolutionNotes"`
	ResolvedAt        *time.Time `json:"resolvedAt,omitempty"`
	CreatedAt         time.Time  `json:"createdAt"`
	UpdatedAt         time.Time  `json:"updatedAt"`
}

// ResolveCaseRequest is the payload from backoffice finance manager to resolve a discrepancy
type ResolveCaseRequest struct {
	CauseCategory           string `json:"causeCategory"`           // e.g. "FEE_ADJUSTMENT"
	ResolutionNotes         string `json:"resolutionNotes"`         // Explanation of root cause and action taken
	CompensatingAccountCode string `json:"compensatingAccountCode"` // Optional debit account (default "6410")
}
