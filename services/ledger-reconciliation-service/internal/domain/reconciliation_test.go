package domain

import "testing"

func TestEvaluateSettlement_SC11(t *testing.T) {
	// SC11: Thu 660.000, phí 1% (6.600) -> kỳ vọng 653.400, thực nhận 650.000 -> chênh 3.400
	res := EvaluateSettlement("O-001", 660000, 1, 650000, "s3://commerce-lab-retail-assets/settlements/2026-10-26.csv")
	if res.PartnerFeeVND != 6600 {
		t.Fatalf("expected fee 6600, got %d", res.PartnerFeeVND)
	}
	if res.ExpectedPayoutVND != 653400 {
		t.Fatalf("expected payout 653400, got %d", res.ExpectedPayoutVND)
	}
	if res.DiscrepancyVND != 3400 || res.Status != "DISCREPANCY_OPEN" {
		t.Fatalf("expected discrepancy 3400 and DISCREPANCY_OPEN, got %+v", res)
	}
}
