package repository

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/services/ledger-reconciliation-service/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrCaseNotFound        = errors.New("reconciliation case not found")
	ErrCaseAlreadyResolved = errors.New("reconciliation case is already resolved")
)

type CaseFilter struct {
	Status  string
	BatchID string
	OrderID string
}

type CaseRepository struct {
	pool *pgxpool.Pool

	mu    sync.Mutex
	cases map[string]*domain.ReconciliationCase
}

func NewCaseRepository(pool *pgxpool.Pool) *CaseRepository {
	return &CaseRepository{
		pool:  pool,
		cases: make(map[string]*domain.ReconciliationCase),
	}
}

type caseExecer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func createCaseWith(ctx context.Context, exec caseExecer, c domain.ReconciliationCase) error {
	_, err := exec.Exec(ctx, `
		INSERT INTO ledger.reconciliation_cases (
			case_id, batch_id, line_number, order_id, transaction_ref,
			expected_payout_vnd, actual_payout_vnd, discrepancy_vnd,
			cause_category, status, assigned_to, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (case_id) DO NOTHING
	`,
		c.CaseID, c.BatchID, c.LineNumber, c.OrderID, c.TransactionRef,
		c.ExpectedPayoutVND, c.ActualPayoutVND, c.DiscrepancyVND,
		c.CauseCategory, c.Status, c.AssignedTo, c.CreatedAt, c.UpdatedAt,
	)
	if err != nil {
		return dependency.Wrap(err)
	}
	return nil
}

// CreateCase persists a new reconciliation case (or ignores if already exists)
func (r *CaseRepository) CreateCase(ctx context.Context, c domain.ReconciliationCase) error {
	if r.pool == nil {
		r.mu.Lock()
		defer r.mu.Unlock()

		if _, exists := r.cases[c.CaseID]; exists {
			return nil
		}
		cp := c
		r.cases[c.CaseID] = &cp
		return nil
	}

	return createCaseWith(ctx, r.pool, c)
}

// ListCases returns cases matching filter, sorted by created_at DESC
func (r *CaseRepository) ListCases(ctx context.Context, filter CaseFilter) ([]domain.ReconciliationCase, error) {
	if r.pool == nil {
		r.mu.Lock()
		defer r.mu.Unlock()

		res := make([]domain.ReconciliationCase, 0, len(r.cases))
		for _, c := range r.cases {
			if filter.Status != "" && c.Status != filter.Status {
				continue
			}
			if filter.BatchID != "" && c.BatchID != filter.BatchID {
				continue
			}
			if filter.OrderID != "" && c.OrderID != filter.OrderID {
				continue
			}
			res = append(res, *c)
		}
		sort.Slice(res, func(i, j int) bool {
			return res[i].CreatedAt.After(res[j].CreatedAt)
		})
		return res, nil
	}

	query := `
		SELECT
			case_id, batch_id, line_number, order_id, transaction_ref,
			expected_payout_vnd, actual_payout_vnd, discrepancy_vnd,
			cause_category, status, assigned_to, resolved_by, resolution_notes,
			resolved_at, created_at, updated_at
		FROM ledger.reconciliation_cases
		WHERE 1=1
	`
	args := []any{}
	idx := 1
	if filter.Status != "" {
		query += fmt.Sprintf(" AND status = $%d", idx)
		args = append(args, filter.Status)
		idx++
	}
	if filter.BatchID != "" {
		query += fmt.Sprintf(" AND batch_id = $%d", idx)
		args = append(args, filter.BatchID)
		idx++
	}
	if filter.OrderID != "" {
		query += fmt.Sprintf(" AND order_id = $%d", idx)
		args = append(args, filter.OrderID)
		idx++
	}
	query += " ORDER BY created_at DESC"

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, dependency.Wrap(err)
	}
	defer rows.Close()

	res := make([]domain.ReconciliationCase, 0)
	for rows.Next() {
		var c domain.ReconciliationCase
		var resolvedAt *time.Time
		err := rows.Scan(
			&c.CaseID, &c.BatchID, &c.LineNumber, &c.OrderID, &c.TransactionRef,
			&c.ExpectedPayoutVND, &c.ActualPayoutVND, &c.DiscrepancyVND,
			&c.CauseCategory, &c.Status, &c.AssignedTo, &c.ResolvedBy, &c.ResolutionNotes,
			&resolvedAt, &c.CreatedAt, &c.UpdatedAt,
		)
		if err != nil {
			return nil, dependency.Wrap(err)
		}
		c.ResolvedAt = resolvedAt
		res = append(res, c)
	}
	return res, rows.Err()
}

// GetCase retrieves a single reconciliation case by ID
func (r *CaseRepository) GetCase(ctx context.Context, caseID string) (domain.ReconciliationCase, error) {
	if r.pool == nil {
		r.mu.Lock()
		defer r.mu.Unlock()

		c, exists := r.cases[caseID]
		if !exists {
			return domain.ReconciliationCase{}, ErrCaseNotFound
		}
		return *c, nil
	}

	return getCaseWith(ctx, r.pool, caseID, false)
}

func getCaseWith(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, caseID string, lock bool) (domain.ReconciliationCase, error) {
	var c domain.ReconciliationCase
	var resolvedAt *time.Time
	query := `
		SELECT
			case_id, batch_id, line_number, order_id, transaction_ref,
			expected_payout_vnd, actual_payout_vnd, discrepancy_vnd,
			cause_category, status, assigned_to, resolved_by, resolution_notes,
			resolved_at, created_at, updated_at
		FROM ledger.reconciliation_cases
		WHERE case_id = $1
	`
	if lock {
		query += " FOR UPDATE"
	}
	err := db.QueryRow(ctx, query, caseID).Scan(
		&c.CaseID, &c.BatchID, &c.LineNumber, &c.OrderID, &c.TransactionRef,
		&c.ExpectedPayoutVND, &c.ActualPayoutVND, &c.DiscrepancyVND,
		&c.CauseCategory, &c.Status, &c.AssignedTo, &c.ResolvedBy, &c.ResolutionNotes,
		&resolvedAt, &c.CreatedAt, &c.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ReconciliationCase{}, ErrCaseNotFound
		}
		return domain.ReconciliationCase{}, dependency.Wrap(err)
	}
	c.ResolvedAt = resolvedAt
	return c, nil
}

// ResolveCase resolves a discrepancy case and posts a compensating double-entry journal entry
func (r *CaseRepository) ResolveCase(
	ctx context.Context,
	caseID string,
	req domain.ResolveCaseRequest,
	actor string,
	ledgerRepo *LedgerRepository,
	settleRepo *SettlementRepository,
) (domain.ReconciliationCase, domain.JournalEntry, error) {
	var c domain.ReconciliationCase
	var tx pgx.Tx
	var err error
	if r.pool == nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		current, found := r.cases[caseID]
		if !found {
			return domain.ReconciliationCase{}, domain.JournalEntry{}, ErrCaseNotFound
		}
		c = *current
	} else {
		tx, err = r.pool.Begin(ctx)
		if err != nil {
			return domain.ReconciliationCase{}, domain.JournalEntry{}, dependency.Wrap(err)
		}
		defer tx.Rollback(ctx)
		c, err = getCaseWith(ctx, tx, caseID, true)
		if err != nil {
			return domain.ReconciliationCase{}, domain.JournalEntry{}, err
		}
	}
	if c.Status == "RESOLVED" {
		return domain.ReconciliationCase{}, domain.JournalEntry{}, ErrCaseAlreadyResolved
	}

	compensatingAccount := req.CompensatingAccountCode
	if compensatingAccount == "" {
		compensatingAccount = "6410" // Default to payment gateway processing fee
	}

	entryID := fmt.Sprintf("je_resolve_%s", caseID)
	entry, err := domain.NewCompensatingAdjustmentEntry(
		entryID,
		c.CaseID,
		c.OrderID,
		c.DiscrepancyVND,
		compensatingAccount,
		req.CauseCategory,
		req.ResolutionNotes,
		actor,
	)
	if err != nil {
		return domain.ReconciliationCase{}, domain.JournalEntry{}, fmt.Errorf("failed to build compensating entry: %w", err)
	}

	now := time.Now().UTC()
	c.Status = "RESOLVED"
	c.CauseCategory = req.CauseCategory
	c.ResolutionNotes = req.ResolutionNotes
	c.ResolvedBy = actor
	c.ResolvedAt = &now
	c.UpdatedAt = now

	if tx == nil {
		if ledgerRepo != nil {
			if _, err := ledgerRepo.RecordJournalEntry(ctx, entry); err != nil {
				return domain.ReconciliationCase{}, domain.JournalEntry{}, err
			}
		}
		if settleRepo != nil {
			if err := settleRepo.UpdateLineMatchStatus(ctx, c.BatchID, c.LineNumber, "RESOLVED"); err != nil {
				return domain.ReconciliationCase{}, domain.JournalEntry{}, err
			}
		}
		r.cases[caseID] = &c
		return c, entry, nil
	}

	if ledgerRepo != nil {
		if err := recordJournalEntryTx(ctx, tx, entry); err != nil {
			return domain.ReconciliationCase{}, domain.JournalEntry{}, err
		}
	}

	tag, err := tx.Exec(ctx, `
		UPDATE ledger.reconciliation_cases
		SET status = 'RESOLVED', cause_category = $1, resolution_notes = $2,
			resolved_by = $3, resolved_at = $4, updated_at = $5
		WHERE case_id = $6 AND status <> 'RESOLVED'
	`, c.CauseCategory, c.ResolutionNotes, c.ResolvedBy, c.ResolvedAt, c.UpdatedAt, caseID)
	if err != nil {
		return domain.ReconciliationCase{}, domain.JournalEntry{}, dependency.Wrap(err)
	}
	if tag.RowsAffected() != 1 {
		return domain.ReconciliationCase{}, domain.JournalEntry{}, ErrCaseAlreadyResolved
	}
	if settleRepo != nil {
		tag, err := tx.Exec(ctx, `UPDATE ledger.settlement_lines SET match_status = 'RESOLVED'
			WHERE batch_id = $1 AND line_number = $2`, c.BatchID, c.LineNumber)
		if err != nil {
			return domain.ReconciliationCase{}, domain.JournalEntry{}, dependency.Wrap(err)
		}
		if tag.RowsAffected() != 1 {
			return domain.ReconciliationCase{}, domain.JournalEntry{}, fmt.Errorf("settlement line %s/%d not found", c.BatchID, c.LineNumber)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ReconciliationCase{}, domain.JournalEntry{}, dependency.Wrap(err)
	}
	return c, entry, nil
}
