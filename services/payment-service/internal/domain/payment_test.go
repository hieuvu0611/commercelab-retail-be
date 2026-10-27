package domain

import "testing"

func TestRefundCeiling_SC08(t *testing.T) {
	eng := NewPaymentEngine()

	// Captured 660,000 VND. First refund: 270,000 VND (SC07 return 1 Backpack).
	if _, err := eng.ReserveRefundCeiling("O-001", 270000); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := eng.CompleteRefund("O-001", 270000); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Second refund: 300,000 VND currently pending gateway confirmation.
	sum, err := eng.ReserveRefundCeiling("O-001", 300000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sum.RemainingRefundable() != 90000 {
		t.Fatalf("expected remaining ceiling 90000, got %d", sum.RemainingRefundable())
	}

	// Another operator attempts to refund an additional 100,000 VND -> must be rejected (SC08).
	if _, err := eng.ReserveRefundCeiling("O-001", 100000); err != ErrRefundCeilingExceeded {
		t.Fatalf("expected ErrRefundCeilingExceeded, got %v", err)
	}
}

func TestUnknown30mAndLateWebhookAutoRefund_SC05(t *testing.T) {
	eng := NewPaymentEngine()

	// 1. Initiate payment attempt for order O-002 (660,000 VND).
	att, err := eng.CreateAttempt("PAY-002", "O-002", "IDEM-PAY-002-A", 660000)
	if err != nil {
		t.Fatalf("unexpected error creating attempt: %v", err)
	}
	if att.Status != "PROCESSING" {
		t.Fatalf("expected status PROCESSING, got %s", att.Status)
	}

	// 2. Gateway times out -> status becomes UNKNOWN with 30-minute deadline.
	_, att, autoRefund, err := eng.ProcessWebhook("O-002", "PAY-002", "UNKNOWN", 660000)
	if err != nil {
		t.Fatalf("unexpected error on UNKNOWN webhook: %v", err)
	}
	if att.Status != "UNKNOWN" || att.UnknownDeadline == nil || autoRefund {
		t.Fatalf("expected UNKNOWN status with deadline and autoRefund=false, got %+v", att)
	}

	// 3. Customer clicks "Pay Again" with a new key while attempt is UNKNOWN -> blocked (SC05).
	if _, err := eng.CreateAttempt("PAY-002-DUP", "O-002", "IDEM-PAY-002-B", 660000); err != ErrActivePaymentInProgress {
		t.Fatalf("expected ErrActivePaymentInProgress during UNKNOWN window, got %v", err)
	}

	// 4. After 30 minutes without confirmation, attempt expires (EXPIRE_30M).
	_, att, _, err = eng.ProcessWebhook("O-002", "PAY-002", "EXPIRE_30M", 660000)
	if err != nil || att.Status != "EXPIRED_30M" {
		t.Fatalf("expected EXPIRED_30M, got %v / %+v", err, att)
	}

	// 5. At minute 40, late CAPTURED webhook arrives -> must trigger autoRefundRequired = true (SC05).
	sum, att, autoRefund, err := eng.ProcessWebhook("O-002", "PAY-002", "CAPTURED", 660000)
	if err != nil {
		t.Fatalf("unexpected error on late CAPTURED webhook: %v", err)
	}
	if !autoRefund || att.Status != "CAPTURED_LATE_REFUND" {
		t.Fatalf("expected autoRefund=true and status CAPTURED_LATE_REFUND, got autoRefund=%v status=%s", autoRefund, att.Status)
	}
	if sum.CapturedAmount != 660000 || sum.RefundPending != 660000 {
		t.Fatalf("expected captured=660000 and refundPending=660000, got %+v", sum)
	}
}
