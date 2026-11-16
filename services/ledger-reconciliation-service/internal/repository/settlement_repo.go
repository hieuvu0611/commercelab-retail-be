package repository

import (
	"context"
	"errors"
	"sort"
	"sync"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/services/ledger-reconciliation-service/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrBatchNotFound = errors.New("settlement batch not found")
)

type SettlementRepository struct {
	pool *pgxpool.Pool

	// In-memory fallback
	mu      sync.Mutex
	batches map[string]*domain.SettlementBatch
	lines   map[string][]domain.SettlementLine // keyed by batchID
	byHash  map[string]string                  // hash -> batchID
}

func NewSettlementRepository(pool *pgxpool.Pool) *SettlementRepository {
	return &SettlementRepository{
		pool:    pool,
		batches: make(map[string]*domain.SettlementBatch),
		lines:   make(map[string][]domain.SettlementLine),
		byHash:  make(map[string]string),
	}
}

// ImportSettlementBatch stores a settlement batch and its lines idempotently.
// If the file hash was already imported, it returns (existingBatch, true, nil) with 0 duplicate rows written.
func (r *SettlementRepository) ImportSettlementBatch(
	ctx context.Context,
	batch domain.SettlementBatch,
	lines []domain.SettlementLine,
) (domain.SettlementBatch, bool, error) {
	if r.pool == nil {
		r.mu.Lock()
		defer r.mu.Unlock()

		// Idempotency check by file hash
		if existingID, exists := r.byHash[batch.FileHash]; exists {
			if existing, ok := r.batches[existingID]; ok {
				return *existing, true, nil
			}
		}

		cpBatch := batch
		r.batches[batch.BatchID] = &cpBatch
		r.byHash[batch.FileHash] = batch.BatchID

		cpLines := make([]domain.SettlementLine, len(lines))
		copy(cpLines, lines)
		r.lines[batch.BatchID] = cpLines

		return batch, false, nil
	}

	// 1. Check if hash exists in PostgreSQL
	var existing domain.SettlementBatch
	err := r.pool.QueryRow(ctx, `
		SELECT batch_id, filename, s3_key, file_hash, total_rows, total_gross_vnd, total_fee_vnd, total_net_vnd, status, imported_by, imported_at, created_at
		FROM settlement_batches WHERE file_hash = $1
	`, batch.FileHash).Scan(
		&existing.BatchID, &existing.Filename, &existing.S3Key, &existing.FileHash,
		&existing.TotalRows, &existing.TotalGrossVND, &existing.TotalFeeVND, &existing.TotalNetVND,
		&existing.Status, &existing.ImportedBy, &existing.ImportedAt, &existing.CreatedAt,
	)
	if err == nil {
		// Already imported! Return existing batch without writing duplicates
		return existing, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.SettlementBatch{}, false, dependency.Wrap(err)
	}

	// 2. Insert new batch and lines inside transaction
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.SettlementBatch{}, false, dependency.Wrap(err)
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, `
		INSERT INTO settlement_batches (
			batch_id, filename, s3_key, file_hash, total_rows, total_gross_vnd, total_fee_vnd, total_net_vnd, status, imported_by, imported_at, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
	`, batch.BatchID, batch.Filename, batch.S3Key, batch.FileHash, batch.TotalRows, batch.TotalGrossVND, batch.TotalFeeVND, batch.TotalNetVND, batch.Status, batch.ImportedBy, batch.ImportedAt, batch.CreatedAt)
	if err != nil {
		return domain.SettlementBatch{}, false, dependency.Wrap(err)
	}

	for _, l := range lines {
		_, err = tx.Exec(ctx, `
			INSERT INTO settlement_lines (
				batch_id, line_number, transaction_ref, order_id, type, gross_amount_vnd, fee_amount_vnd, net_amount_vnd, transacted_at, match_status, created_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		`, batch.BatchID, l.LineNumber, l.TransactionRef, l.OrderID, l.Type, l.GrossAmountVND, l.FeeAmountVND, l.NetAmountVND, l.TransactedAt, l.MatchStatus, l.CreatedAt)
		if err != nil {
			return domain.SettlementBatch{}, false, dependency.Wrap(err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return domain.SettlementBatch{}, false, dependency.Wrap(err)
	}

	return batch, false, nil
}

// GetSettlementBatch retrieves a batch by ID
func (r *SettlementRepository) GetSettlementBatch(ctx context.Context, batchID string) (domain.SettlementBatch, error) {
	if r.pool == nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		b, ok := r.batches[batchID]
		if !ok {
			return domain.SettlementBatch{}, ErrBatchNotFound
		}
		return *b, nil
	}

	var b domain.SettlementBatch
	err := r.pool.QueryRow(ctx, `
		SELECT batch_id, filename, s3_key, file_hash, total_rows, total_gross_vnd, total_fee_vnd, total_net_vnd, status, imported_by, imported_at, created_at
		FROM settlement_batches WHERE batch_id = $1
	`, batchID).Scan(
		&b.BatchID, &b.Filename, &b.S3Key, &b.FileHash,
		&b.TotalRows, &b.TotalGrossVND, &b.TotalFeeVND, &b.TotalNetVND,
		&b.Status, &b.ImportedBy, &b.ImportedAt, &b.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.SettlementBatch{}, ErrBatchNotFound
		}
		return domain.SettlementBatch{}, dependency.Wrap(err)
	}
	return b, nil
}

// ListSettlementBatches returns batches sorted newest first
func (r *SettlementRepository) ListSettlementBatches(ctx context.Context, limit, offset int) ([]domain.SettlementBatch, int, error) {
	if limit <= 0 {
		limit = 50
	}

	if r.pool == nil {
		r.mu.Lock()
		defer r.mu.Unlock()

		list := make([]domain.SettlementBatch, 0, len(r.batches))
		for _, b := range r.batches {
			list = append(list, *b)
		}
		sort.Slice(list, func(i, j int) bool {
			return list[i].ImportedAt.After(list[j].ImportedAt)
		})

		total := len(list)
		if offset >= total {
			return []domain.SettlementBatch{}, total, nil
		}
		end := offset + limit
		if end > total {
			end = total
		}
		return list[offset:end], total, nil
	}

	var total int
	if err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM settlement_batches`).Scan(&total); err != nil {
		return nil, 0, dependency.Wrap(err)
	}

	rows, err := r.pool.Query(ctx, `
		SELECT batch_id, filename, s3_key, file_hash, total_rows, total_gross_vnd, total_fee_vnd, total_net_vnd, status, imported_by, imported_at, created_at
		FROM settlement_batches
		ORDER BY imported_at DESC
		LIMIT $1 OFFSET $2
	`, limit, offset)
	if err != nil {
		return nil, 0, dependency.Wrap(err)
	}
	defer rows.Close()

	list := make([]domain.SettlementBatch, 0)
	for rows.Next() {
		var b domain.SettlementBatch
		if err := rows.Scan(
			&b.BatchID, &b.Filename, &b.S3Key, &b.FileHash,
			&b.TotalRows, &b.TotalGrossVND, &b.TotalFeeVND, &b.TotalNetVND,
			&b.Status, &b.ImportedBy, &b.ImportedAt, &b.CreatedAt,
		); err != nil {
			return nil, 0, dependency.Wrap(err)
		}
		list = append(list, b)
	}
	return list, total, nil
}

// ListSettlementLines returns all lines for a batch
func (r *SettlementRepository) ListSettlementLines(ctx context.Context, batchID string) ([]domain.SettlementLine, error) {
	if r.pool == nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		lines, ok := r.lines[batchID]
		if !ok {
			return []domain.SettlementLine{}, nil
		}
		cp := make([]domain.SettlementLine, len(lines))
		copy(cp, lines)
		return cp, nil
	}

	rows, err := r.pool.Query(ctx, `
		SELECT line_id, batch_id, line_number, transaction_ref, order_id, type, gross_amount_vnd, fee_amount_vnd, net_amount_vnd, transacted_at, match_status, created_at
		FROM settlement_lines
		WHERE batch_id = $1
		ORDER BY line_number ASC
	`, batchID)
	if err != nil {
		return nil, dependency.Wrap(err)
	}
	defer rows.Close()

	lines := make([]domain.SettlementLine, 0)
	for rows.Next() {
		var l domain.SettlementLine
		if err := rows.Scan(
			&l.LineID, &l.BatchID, &l.LineNumber, &l.TransactionRef, &l.OrderID, &l.Type,
			&l.GrossAmountVND, &l.FeeAmountVND, &l.NetAmountVND, &l.TransactedAt, &l.MatchStatus, &l.CreatedAt,
		); err != nil {
			return nil, dependency.Wrap(err)
		}
		lines = append(lines, l)
	}
	return lines, nil
}

// UpdateLineMatchStatus updates the matching status of a line
func (r *SettlementRepository) UpdateLineMatchStatus(ctx context.Context, batchID string, lineNumber int, status string) error {
	if r.pool == nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		lines := r.lines[batchID]
		for i := range lines {
			if lines[i].LineNumber == lineNumber {
				lines[i].MatchStatus = status
				return nil
			}
		}
		return nil
	}

	_, err := r.pool.Exec(ctx, `
		UPDATE settlement_lines SET match_status = $3
		WHERE batch_id = $1 AND line_number = $2
	`, batchID, lineNumber, status)
	if err != nil {
		return dependency.Wrap(err)
	}
	return nil
}

// UpdateBatchStatus updates the status of a settlement batch
func (r *SettlementRepository) UpdateBatchStatus(ctx context.Context, batchID string, status string) error {
	if r.pool == nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		if b, ok := r.batches[batchID]; ok {
			b.Status = status
		}
		return nil
	}

	_, err := r.pool.Exec(ctx, `UPDATE settlement_batches SET status = $2 WHERE batch_id = $1`, batchID, status)
	if err != nil {
		return dependency.Wrap(err)
	}
	return nil
}
