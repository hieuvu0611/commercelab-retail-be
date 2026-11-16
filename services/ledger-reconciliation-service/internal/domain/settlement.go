package domain

import (
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

var (
	ErrEmptyCSV       = errors.New("settlement CSV is empty")
	ErrInvalidHeader  = errors.New("invalid settlement CSV header")
	ErrInvalidCSVLine = errors.New("invalid settlement line format")
)

type SettlementBatch struct {
	BatchID       string    `json:"batchId"`
	Filename      string    `json:"filename"`
	S3Key         string    `json:"s3Key"`
	FileHash      string    `json:"fileHash"`
	TotalRows     int       `json:"totalRows"`
	TotalGrossVND int64     `json:"totalGrossVnd"`
	TotalFeeVND   int64     `json:"totalFeeVnd"`
	TotalNetVND   int64     `json:"totalNetVnd"`
	Status        string    `json:"status"`
	ImportedBy    string    `json:"importedBy"`
	ImportedAt    time.Time `json:"importedAt"`
	CreatedAt     time.Time `json:"createdAt"`
}

type SettlementLine struct {
	LineID         int64     `json:"lineId,omitempty"`
	BatchID        string    `json:"batchId"`
	LineNumber     int       `json:"lineNumber"`
	TransactionRef string    `json:"transactionRef"`
	OrderID        string    `json:"orderId"`
	Type           string    `json:"type"` // COLLECTION, REFUND, FEE, PAYOUT
	GrossAmountVND int64     `json:"grossAmountVnd"`
	FeeAmountVND   int64     `json:"feeAmountVnd"`
	NetAmountVND   int64     `json:"netAmountVnd"`
	TransactedAt   time.Time `json:"transactedAt"`
	MatchStatus    string    `json:"matchStatus"` // PENDING, MATCHED, DISCREPANCY
	CreatedAt      time.Time `json:"createdAt,omitempty"`
}

// ParseSettlementCSV reads CSV data, calculates SHA-256 hash, and extracts structured batch & line models
func ParseSettlementCSV(r io.Reader, filename, s3Key, importedBy string) (SettlementBatch, []SettlementLine, error) {
	hasher := sha256.New()
	tee := io.TeeReader(r, hasher)
	reader := csv.NewReader(tee)
	reader.TrimLeadingSpace = true

	records, err := reader.ReadAll()
	if err != nil {
		return SettlementBatch{}, nil, fmt.Errorf("read csv: %w", err)
	}

	if len(records) < 2 {
		return SettlementBatch{}, nil, ErrEmptyCSV
	}

	header := records[0]
	colIdx := make(map[string]int)
	for i, h := range header {
		colIdx[strings.ToLower(strings.TrimSpace(h))] = i
	}

	requiredCols := []string{"transaction_ref", "order_id", "type", "gross_amount_vnd", "net_amount_vnd", "transacted_at"}
	for _, req := range requiredCols {
		if _, ok := colIdx[req]; !ok {
			return SettlementBatch{}, nil, fmt.Errorf("%w: missing required column %q", ErrInvalidHeader, req)
		}
	}

	feeColIdx, hasFee := colIdx["fee_amount_vnd"]

	now := time.Now().UTC()
	hashStr := hex.EncodeToString(hasher.Sum(nil))
	batchID := fmt.Sprintf("BATCH-%s-%s", now.Format("20060102"), hashStr[:8])

	lines := make([]SettlementLine, 0, len(records)-1)
	var totalGross, totalFee, totalNet int64

	for idx, row := range records[1:] {
		lineNum := idx + 1
		txRef := strings.TrimSpace(row[colIdx["transaction_ref"]])
		orderID := strings.TrimSpace(row[colIdx["order_id"]])
		txType := strings.ToUpper(strings.TrimSpace(row[colIdx["type"]]))

		if txRef == "" || orderID == "" {
			return SettlementBatch{}, nil, fmt.Errorf("%w at line %d: transaction_ref and order_id are required", ErrInvalidCSVLine, lineNum)
		}

		gross, err := strconv.ParseInt(strings.TrimSpace(row[colIdx["gross_amount_vnd"]]), 10, 64)
		if err != nil {
			return SettlementBatch{}, nil, fmt.Errorf("%w at line %d: invalid gross_amount_vnd", ErrInvalidCSVLine, lineNum)
		}

		var fee int64
		if hasFee && feeColIdx < len(row) && strings.TrimSpace(row[feeColIdx]) != "" {
			fee, _ = strconv.ParseInt(strings.TrimSpace(row[feeColIdx]), 10, 64)
		}

		net, err := strconv.ParseInt(strings.TrimSpace(row[colIdx["net_amount_vnd"]]), 10, 64)
		if err != nil {
			return SettlementBatch{}, nil, fmt.Errorf("%w at line %d: invalid net_amount_vnd", ErrInvalidCSVLine, lineNum)
		}

		txTimeStr := strings.TrimSpace(row[colIdx["transacted_at"]])
		txTime, err := parseTimestamp(txTimeStr)
		if err != nil {
			return SettlementBatch{}, nil, fmt.Errorf("%w at line %d: invalid transacted_at: %v", ErrInvalidCSVLine, lineNum, err)
		}

		line := SettlementLine{
			BatchID:        batchID,
			LineNumber:     lineNum,
			TransactionRef: txRef,
			OrderID:        orderID,
			Type:           txType,
			GrossAmountVND: gross,
			FeeAmountVND:   fee,
			NetAmountVND:   net,
			TransactedAt:   txTime,
			MatchStatus:    "PENDING",
			CreatedAt:      now,
		}

		lines = append(lines, line)
		totalGross += gross
		totalFee += fee
		totalNet += net
	}

	batch := SettlementBatch{
		BatchID:       batchID,
		Filename:      filename,
		S3Key:         s3Key,
		FileHash:      hashStr,
		TotalRows:     len(lines),
		TotalGrossVND: totalGross,
		TotalFeeVND:   totalFee,
		TotalNetVND:   totalNet,
		Status:        "IMPORTED",
		ImportedBy:    importedBy,
		ImportedAt:    now,
		CreatedAt:     now,
	}

	return batch, lines, nil
}

func parseTimestamp(val string) (time.Time, error) {
	formats := []string{
		time.RFC3339,
		"2006-01-02T15:04:05Z07:00",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, val); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unable to parse date string: %s", val)
}
