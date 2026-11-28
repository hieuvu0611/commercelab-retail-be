package domain

import (
	"testing"
)

func TestValidateJournalEntry(t *testing.T) {
	t.Run("valid balanced 2-line entry passes", func(t *testing.T) {
		entry, err := NewPaymentCapturedEntry("JE-01", "ORD-123", 660_000, "staff")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if err := ValidateJournalEntry(entry); err != nil {
			t.Fatalf("expected valid entry, got: %v", err)
		}
	})

	t.Run("unbalanced entry is rejected", func(t *testing.T) {
		entry := JournalEntry{
			EntryID:     "JE-BAD",
			EventType:   "MANUAL",
			ReferenceID: "REF-1",
			Postings: []Posting{
				{AccountCode: "1120", Direction: DirectionDebit, AmountVND: 100_000},
				{AccountCode: "5110", Direction: DirectionCredit, AmountVND: 90_000},
			},
		}
		err := ValidateJournalEntry(entry)
		if err == nil {
			t.Fatal("expected unbalanced entry error, got nil")
		}
	})

	t.Run("entry with fewer than 2 postings is rejected", func(t *testing.T) {
		entry := JournalEntry{
			EntryID:     "JE-ONE",
			EventType:   "MANUAL",
			ReferenceID: "REF-1",
			Postings: []Posting{
				{AccountCode: "1120", Direction: DirectionDebit, AmountVND: 100_000},
			},
		}
		if err := ValidateJournalEntry(entry); err != ErrInsufficientLines {
			t.Fatalf("expected ErrInsufficientLines, got: %v", err)
		}
	})

	t.Run("entry with zero or negative amounts is rejected", func(t *testing.T) {
		entry := JournalEntry{
			EntryID:     "JE-ZERO",
			EventType:   "MANUAL",
			ReferenceID: "REF-1",
			Postings: []Posting{
				{AccountCode: "1120", Direction: DirectionDebit, AmountVND: 0},
				{AccountCode: "5110", Direction: DirectionCredit, AmountVND: 0},
			},
		}
		if err := ValidateJournalEntry(entry); err != ErrInvalidPosting {
			t.Fatalf("expected ErrInvalidPosting, got: %v", err)
		}
	})
}

func TestNewSettlementMatchedEntry_SC11(t *testing.T) {
	t.Run("SC11 exact matched with 3,400 discrepancy preserves balance", func(t *testing.T) {
		// Gross 660.000, Fee 6.600, Actual Payout 650.000, Discrepancy 3.400
		entry, err := NewSettlementMatchedEntry(
			"JE-SC11", "ORD-660K", "BATCH-001",
			660_000, 6_600, 650_000, 3_400, "finance-mgr",
		)
		if err != nil {
			t.Fatalf("failed to create SC11 settlement entry: %v", err)
		}

		if len(entry.Postings) != 4 {
			t.Fatalf("expected 4 postings (Bank, Fee, Discrepancy, Gateway AR), got %d", len(entry.Postings))
		}

		// Sum debits: 650.000 + 6.600 + 3.400 = 660.000
		// Sum credits: 660.000
		var debits, credits int64
		for _, p := range entry.Postings {
			if p.Direction == DirectionDebit {
				debits += p.AmountVND
			} else {
				credits += p.AmountVND
			}
		}

		if debits != 660_000 || credits != 660_000 {
			t.Fatalf("expected balanced debits (%d) and credits (%d) of 660.000", debits, credits)
		}
	})

	t.Run("Standard matched settlement without discrepancy", func(t *testing.T) {
		entry, err := NewSettlementMatchedEntry(
			"JE-MATCH", "ORD-NORMAL", "BATCH-002",
			1_000_000, 10_000, 990_000, 0, "finance-mgr",
		)
		if err != nil {
			t.Fatalf("failed to create standard settlement entry: %v", err)
		}
		if len(entry.Postings) != 3 {
			t.Fatalf("expected 3 postings (Bank, Fee, Gateway AR), got %d", len(entry.Postings))
		}
	})
}

func TestNewCODDeliveryAndSettlementEntries(t *testing.T) {
	t.Run("COD delivery confirmation entry is balanced", func(t *testing.T) {
		entry, err := NewCODDeliveryConfirmedEntry("JE-COD-DELIV", "O-100", "GHN", 1_500_000, "staff")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if err := ValidateJournalEntry(entry); err != nil {
			t.Fatalf("expected valid entry, got: %v", err)
		}
		if len(entry.Postings) != 2 {
			t.Fatalf("expected 2 postings, got %d", len(entry.Postings))
		}
		if entry.Postings[0].AccountCode != "1310" || entry.Postings[0].Direction != DirectionDebit {
			t.Errorf("expected debit 1310, got %+v", entry.Postings[0])
		}
		if entry.Postings[1].AccountCode != "5110" || entry.Postings[1].Direction != DirectionCredit {
			t.Errorf("expected credit 5110, got %+v", entry.Postings[1])
		}
	})

	t.Run("COD settlement matched entry is balanced", func(t *testing.T) {
		// Gross 1.500.000, fee 15.000, net 1.485.000
		entry, err := NewCODSettlementMatchedEntry("JE-COD-SETTLE", "O-100", "BATCH-COD-1", "GHN", 1_500_000, 15_000, 1_485_000, 0, "finance-mgr")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if err := ValidateJournalEntry(entry); err != nil {
			t.Fatalf("expected valid entry, got: %v", err)
		}
		var debits, credits int64
		for _, p := range entry.Postings {
			if p.Direction == DirectionDebit {
				debits += p.AmountVND
			} else {
				credits += p.AmountVND
			}
		}
		if debits != 1_500_000 || credits != 1_500_000 {
			t.Fatalf("expected balanced debits (%d) and credits (%d)", debits, credits)
		}
	})
}

func TestNewPOSSaleCompletedEntry(t *testing.T) {
	t.Run("POS_CASH debits 1111 and credits 5110", func(t *testing.T) {
		entry, err := NewPOSSaleCompletedEntry("JE-POS-1", "ord-pos-1", "WH-HCM-01", "POS_CASH", 300_000, "cashier-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if err := ValidateJournalEntry(entry); err != nil {
			t.Fatalf("expected balanced entry: %v", err)
		}
		if len(entry.Postings) != 2 || entry.Postings[0].AccountCode != "1111" || entry.Postings[0].Direction != DirectionDebit ||
			entry.Postings[1].AccountCode != "5110" || entry.Postings[1].Direction != DirectionCredit {
			t.Fatalf("unexpected postings: %+v", entry.Postings)
		}
	})

	t.Run("POS_CARD debits 1120 and credits 5110", func(t *testing.T) {
		entry, err := NewPOSSaleCompletedEntry("JE-POS-2", "ord-pos-2", "WH-HN-01", "POS_CARD", 450_000, "cashier-2")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if err := ValidateJournalEntry(entry); err != nil {
			t.Fatalf("expected balanced entry: %v", err)
		}
		if len(entry.Postings) != 2 || entry.Postings[0].AccountCode != "1120" || entry.Postings[0].Direction != DirectionDebit ||
			entry.Postings[1].AccountCode != "5110" || entry.Postings[1].Direction != DirectionCredit {
			t.Fatalf("unexpected postings: %+v", entry.Postings)
		}
	})

	t.Run("unsupported payment method is rejected", func(t *testing.T) {
		if _, err := NewPOSSaleCompletedEntry("JE-POS-3", "ord-pos-3", "WH-HCM-01", "COD", 100_000, "cashier-1"); err == nil {
			t.Fatal("expected error for unsupported payment method")
		}
	})
}
