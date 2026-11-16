package domain

import (
	"strings"
	"testing"
)

func TestParseSettlementCSV(t *testing.T) {
	t.Run("valid settlement CSV with SC11 transaction", func(t *testing.T) {
		csvContent := `transaction_ref,order_id,type,gross_amount_vnd,fee_amount_vnd,net_amount_vnd,transacted_at
TX-VNPAY-001,O-660K,COLLECTION,660000,6600,650000,2026-11-16T15:30:00+07:00
TX-VNPAY-002,O-100K,COLLECTION,100000,1000,99000,2026-11-16T16:00:00+07:00
`
		batch, lines, err := ParseSettlementCSV(strings.NewReader(csvContent), "settlement-2026-11-16.csv", "s3://bucket/settlement.csv", "finance-user")
		if err != nil {
			t.Fatalf("unexpected error parsing valid CSV: %v", err)
		}

		if batch.TotalRows != 2 {
			t.Fatalf("expected 2 rows, got %d", batch.TotalRows)
		}
		if batch.TotalGrossVND != 760_000 {
			t.Fatalf("expected 760,000 gross, got %d", batch.TotalGrossVND)
		}
		if batch.TotalFeeVND != 7_600 {
			t.Fatalf("expected 7,600 fee, got %d", batch.TotalFeeVND)
		}
		if batch.TotalNetVND != 749_000 {
			t.Fatalf("expected 749,000 net, got %d", batch.TotalNetVND)
		}
		if batch.FileHash == "" {
			t.Fatal("expected non-empty file hash")
		}

		if len(lines) != 2 {
			t.Fatalf("expected 2 lines, got %d", len(lines))
		}
		if lines[0].TransactionRef != "TX-VNPAY-001" || lines[0].OrderID != "O-660K" {
			t.Fatalf("unexpected line 1: %+v", lines[0])
		}
		if lines[0].NetAmountVND != 650_000 {
			t.Fatalf("expected net 650,000 in line 1 (SC11), got %d", lines[0].NetAmountVND)
		}
	})

	t.Run("missing required header is rejected", func(t *testing.T) {
		csvContent := `order_id,gross_amount_vnd,net_amount_vnd
O-1,100,100
`
		_, _, err := ParseSettlementCSV(strings.NewReader(csvContent), "bad.csv", "", "user")
		if err == nil {
			t.Fatal("expected error for missing headers, got nil")
		}
	})

	t.Run("empty CSV is rejected", func(t *testing.T) {
		csvContent := ``
		_, _, err := ParseSettlementCSV(strings.NewReader(csvContent), "empty.csv", "", "user")
		if err == nil {
			t.Fatal("expected error for empty csv, got nil")
		}
	})
}
