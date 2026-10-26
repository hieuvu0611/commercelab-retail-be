package domain

// ReconciliationResult thực thi B07, I06, SC11: Đối soát tiền thu, phí 1% và tiền đối tác thực chuyển
type ReconciliationResult struct {
	OrderID             string `json:"orderId"`
	VerifiedCapturedVND int64  `json:"verifiedCapturedVnd"` // VD: 660.000
	PartnerFeeVND       int64  `json:"partnerFeeVnd"`       // VD: 6.600 (1%)
	ExpectedPayoutVND   int64  `json:"expectedPayoutVnd"`   // VD: 653.400
	ActualPayoutVND     int64  `json:"actualPayoutVnd"`     // VD: 650.000
	DiscrepancyVND      int64  `json:"discrepancyVnd"`      // VD: 3.400
	Status              string `json:"status"`              // MATCHED hoặc DISCREPANCY_OPEN
	S3StatementKey      string `json:"s3StatementKey"`
}

func EvaluateSettlement(orderID string, verifiedCapturedVND int64, feePct int64, actualPayoutVND int64, s3Key string) ReconciliationResult {
	fee := (verifiedCapturedVND * feePct) / 100
	expected := verifiedCapturedVND - fee
	diff := expected - actualPayoutVND
	status := "MATCHED"
	if diff != 0 {
		status = "DISCREPANCY_OPEN"
	}
	return ReconciliationResult{
		OrderID:             orderID,
		VerifiedCapturedVND: verifiedCapturedVND,
		PartnerFeeVND:       fee,
		ExpectedPayoutVND:   expected,
		ActualPayoutVND:     actualPayoutVND,
		DiscrepancyVND:      diff,
		Status:              status,
		S3StatementKey:      s3Key,
	}
}
