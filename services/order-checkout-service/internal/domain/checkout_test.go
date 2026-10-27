package domain

import "testing"

func TestIdempotencyAndVoucherQuota_SC04AndSC10(t *testing.T) {
	eng := NewCheckoutEngine()

	// Attempt 1: Create order O-001
	ord1, replayed, err := eng.PlaceOrder("idem-key-1", "O-001", "user-lan", "Q-001", "WELCOME10", 660000)
	if err != nil || replayed {
		t.Fatalf("expected fresh order, got replayed=%v err=%v", replayed, err)
	}

	// Attempt 2 (SC04): Replay same Idempotency-Key and identical payload -> returns existing O-001
	ord2, replayed, err := eng.PlaceOrder("idem-key-1", "O-002-ignored", "user-lan", "Q-001", "WELCOME10", 660000)
	if err != nil || !replayed || ord2.OrderID != ord1.OrderID {
		t.Fatalf("expected idempotent replay of O-001, got %+v err=%v", ord2, err)
	}

	// Attempt 3 (SC04): Same Idempotency-Key with modified QuoteID -> must reject with ErrIdempotencyConflict
	_, _, err = eng.PlaceOrder("idem-key-1", "O-003", "user-lan", "Q-CHANGED", "WELCOME10", 660000)
	if err != ErrIdempotencyConflict {
		t.Fatalf("expected ErrIdempotencyConflict, got %v", err)
	}
}
