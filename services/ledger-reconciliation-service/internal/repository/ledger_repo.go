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
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrEntryNotFound   = errors.New("journal entry not found")
	ErrAccountNotFound = errors.New("account not found")
)

type LedgerRepository struct {
	pool *pgxpool.Pool
	now  func() time.Time

	// In-memory fallback
	mu       sync.Mutex
	accounts map[string]*domain.Account
	entries  map[string]*domain.JournalEntry
}

func NewLedgerRepository(pool *pgxpool.Pool) *LedgerRepository {
	repo := &LedgerRepository{
		pool:     pool,
		now:      time.Now,
		accounts: make(map[string]*domain.Account),
		entries:  make(map[string]*domain.JournalEntry),
	}

	// Default chart of accounts
	repo.seedDefaultAccounts()
	return repo
}

func (r *LedgerRepository) seedDefaultAccounts() {
	now := time.Now().UTC()
	defaults := []domain.Account{
		{AccountCode: "1110", AccountName: "Cash in Bank (Settlement Account)", AccountType: domain.AccountAsset, BalanceVND: 0, CreatedAt: now},
		{AccountCode: "1120", AccountName: "Payment Gateway Receivable (VNPay / Stripe)", AccountType: domain.AccountAsset, BalanceVND: 0, CreatedAt: now},
		{AccountCode: "1388", AccountName: "Reconciliation Discrepancies Receivable", AccountType: domain.AccountAsset, BalanceVND: 0, CreatedAt: now},
		{AccountCode: "3388", AccountName: "Customer Refund Obligations", AccountType: domain.AccountLiability, BalanceVND: 0, CreatedAt: now},
		{AccountCode: "3389", AccountName: "Settlement Overpayments Payable", AccountType: domain.AccountLiability, CreatedAt: now},
		{AccountCode: "1310", AccountName: "3PL COD Receivables", AccountType: domain.AccountAsset, CreatedAt: now},
		{AccountCode: "6420", AccountName: "Carrier COD Processing Fees", AccountType: domain.AccountExpense, CreatedAt: now},
		{AccountCode: "5110", AccountName: "Gross Sales Revenue", AccountType: domain.AccountRevenue, BalanceVND: 0, CreatedAt: now},
		{AccountCode: "5210", AccountName: "Sales Returns & Deductions", AccountType: domain.AccountRevenue, BalanceVND: 0, CreatedAt: now},
		{AccountCode: "6410", AccountName: "Payment Gateway Processing Fees", AccountType: domain.AccountExpense, BalanceVND: 0, CreatedAt: now},
	}
	for _, a := range defaults {
		cp := a
		r.accounts[a.AccountCode] = &cp
	}
}

// RecordJournalEntry records a balanced double-entry and atomically updates account balances
func (r *LedgerRepository) RecordJournalEntry(ctx context.Context, entry domain.JournalEntry) (domain.JournalEntry, error) {
	if err := domain.ValidateJournalEntry(entry); err != nil {
		return domain.JournalEntry{}, err
	}

	now := r.now().UTC()
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = now
	}
	if entry.PostedAt.IsZero() {
		entry.PostedAt = now
	}

	if r.pool == nil {
		r.mu.Lock()
		defer r.mu.Unlock()

		if stored, exists := r.entries[entry.EntryID]; exists {
			return *stored, nil
		}

		// Verify accounts exist and calculate delta
		for _, p := range entry.Postings {
			if _, ok := r.accounts[p.AccountCode]; !ok {
				return domain.JournalEntry{}, fmt.Errorf("%w: %s", ErrAccountNotFound, p.AccountCode)
			}
		}
		for _, p := range entry.Postings {
			acc := r.accounts[p.AccountCode]
			var delta int64
			if acc.AccountType == domain.AccountAsset || acc.AccountType == domain.AccountExpense {
				if p.Direction == domain.DirectionDebit {
					delta = p.AmountVND
				} else {
					delta = -p.AmountVND
				}
			} else {
				if p.Direction == domain.DirectionCredit {
					delta = p.AmountVND
				} else {
					delta = -p.AmountVND
				}
			}
			acc.BalanceVND += delta
		}

		cp := entry
		r.entries[entry.EntryID] = &cp
		return entry, nil
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.JournalEntry{}, dependency.Wrap(err)
	}
	defer tx.Rollback(ctx)

	if err := recordJournalEntryTx(ctx, tx, entry); err != nil {
		return domain.JournalEntry{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return domain.JournalEntry{}, dependency.Wrap(err)
	}

	return entry, nil
}

func recordJournalEntryTx(ctx context.Context, tx pgx.Tx, entry domain.JournalEntry) error {
	tag, err := tx.Exec(ctx, `
		INSERT INTO journal_entries (entry_id, event_type, reference_id, description, posted_at, created_by, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (entry_id) DO NOTHING
	`, entry.EntryID, entry.EventType, entry.ReferenceID, entry.Description, entry.PostedAt, entry.CreatedBy, entry.CreatedAt)
	if err != nil {
		return dependency.Wrap(err)
	}
	if tag.RowsAffected() == 0 {
		return nil
	}

	for _, p := range entry.Postings {
		_, err = tx.Exec(ctx, `
			INSERT INTO ledger_postings (entry_id, account_code, direction, amount_vnd, created_at)
			VALUES ($1, $2, $3, $4, $5)
		`, entry.EntryID, p.AccountCode, p.Direction, p.AmountVND, entry.CreatedAt)
		if err != nil {
			return dependency.Wrap(err)
		}

		// Update account balance based on account type
		var deltaSQL string
		if p.Direction == domain.DirectionDebit {
			deltaSQL = `
				UPDATE accounts SET balance_vnd = CASE
					WHEN account_type IN ('ASSET', 'EXPENSE') THEN balance_vnd + $2
					ELSE balance_vnd - $2
				END WHERE account_code = $1
			`
		} else {
			deltaSQL = `
				UPDATE accounts SET balance_vnd = CASE
					WHEN account_type IN ('ASSET', 'EXPENSE') THEN balance_vnd - $2
					ELSE balance_vnd + $2
				END WHERE account_code = $1
			`
		}
		res, err := tx.Exec(ctx, deltaSQL, p.AccountCode, p.AmountVND)
		if err != nil {
			return dependency.Wrap(err)
		}
		if res.RowsAffected() == 0 {
			return fmt.Errorf("%w: %s", ErrAccountNotFound, p.AccountCode)
		}
	}
	return nil
}

// ListJournalEntries returns all journal entries matching optional referenceID filter
func (r *LedgerRepository) ListJournalEntries(ctx context.Context, referenceID string, limit, offset int) ([]domain.JournalEntry, int, error) {
	if limit <= 0 {
		limit = 50
	}

	if r.pool == nil {
		r.mu.Lock()
		defer r.mu.Unlock()

		filtered := make([]domain.JournalEntry, 0)
		for _, e := range r.entries {
			if referenceID == "" || e.ReferenceID == referenceID {
				filtered = append(filtered, *e)
			}
		}

		sort.Slice(filtered, func(i, j int) bool {
			return filtered[i].PostedAt.After(filtered[j].PostedAt)
		})

		total := len(filtered)
		if offset >= total {
			return []domain.JournalEntry{}, total, nil
		}
		end := offset + limit
		if end > total {
			end = total
		}
		return filtered[offset:end], total, nil
	}

	var total int
	countQuery := `SELECT COUNT(*) FROM journal_entries WHERE ($1 = '' OR reference_id = $1)`
	if err := r.pool.QueryRow(ctx, countQuery, referenceID).Scan(&total); err != nil {
		return nil, 0, dependency.Wrap(err)
	}

	query := `
		SELECT entry_id, event_type, reference_id, description, posted_at, created_by, created_at
		FROM journal_entries
		WHERE ($1 = '' OR reference_id = $1)
		ORDER BY posted_at DESC
		LIMIT $2 OFFSET $3
	`
	rows, err := r.pool.Query(ctx, query, referenceID, limit, offset)
	if err != nil {
		return nil, 0, dependency.Wrap(err)
	}
	defer rows.Close()

	entries := make([]domain.JournalEntry, 0)
	for rows.Next() {
		var e domain.JournalEntry
		if err := rows.Scan(&e.EntryID, &e.EventType, &e.ReferenceID, &e.Description, &e.PostedAt, &e.CreatedBy, &e.CreatedAt); err != nil {
			return nil, 0, dependency.Wrap(err)
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, dependency.Wrap(err)
	}

	// Fetch postings for each entry
	for i := range entries {
		postingsQuery := `
			SELECT posting_id, entry_id, account_code, direction, amount_vnd, created_at
			FROM ledger_postings
			WHERE entry_id = $1
			ORDER BY posting_id ASC
		`
		pRows, err := r.pool.Query(ctx, postingsQuery, entries[i].EntryID)
		if err != nil {
			return nil, 0, dependency.Wrap(err)
		}
		postings := make([]domain.Posting, 0)
		for pRows.Next() {
			var p domain.Posting
			if err := pRows.Scan(&p.PostingID, &p.EntryID, &p.AccountCode, &p.Direction, &p.AmountVND, &p.CreatedAt); err != nil {
				pRows.Close()
				return nil, 0, dependency.Wrap(err)
			}
			postings = append(postings, p)
		}
		pRows.Close()
		entries[i].Postings = postings
	}

	return entries, total, nil
}

// GetJournalEntry fetches a single journal entry by ID with its postings
func (r *LedgerRepository) GetJournalEntry(ctx context.Context, entryID string) (domain.JournalEntry, error) {
	if r.pool == nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		e, ok := r.entries[entryID]
		if !ok {
			return domain.JournalEntry{}, ErrEntryNotFound
		}
		return *e, nil
	}

	var e domain.JournalEntry
	err := r.pool.QueryRow(ctx, `
		SELECT entry_id, event_type, reference_id, description, posted_at, created_by, created_at
		FROM journal_entries WHERE entry_id = $1
	`, entryID).Scan(&e.EntryID, &e.EventType, &e.ReferenceID, &e.Description, &e.PostedAt, &e.CreatedBy, &e.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.JournalEntry{}, ErrEntryNotFound
		}
		return domain.JournalEntry{}, dependency.Wrap(err)
	}

	pRows, err := r.pool.Query(ctx, `
		SELECT posting_id, entry_id, account_code, direction, amount_vnd, created_at
		FROM ledger_postings WHERE entry_id = $1 ORDER BY posting_id ASC
	`, entryID)
	if err != nil {
		return domain.JournalEntry{}, dependency.Wrap(err)
	}
	defer pRows.Close()

	for pRows.Next() {
		var p domain.Posting
		if err := pRows.Scan(&p.PostingID, &p.EntryID, &p.AccountCode, &p.Direction, &p.AmountVND, &p.CreatedAt); err != nil {
			return domain.JournalEntry{}, dependency.Wrap(err)
		}
		e.Postings = append(e.Postings, p)
	}

	return e, nil
}

// ListAccounts returns the chart of accounts and current balances
func (r *LedgerRepository) ListAccounts(ctx context.Context) ([]domain.Account, error) {
	if r.pool == nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		list := make([]domain.Account, 0, len(r.accounts))
		for _, a := range r.accounts {
			list = append(list, *a)
		}
		sort.Slice(list, func(i, j int) bool {
			return list[i].AccountCode < list[j].AccountCode
		})
		return list, nil
	}

	rows, err := r.pool.Query(ctx, `
		SELECT account_code, account_name, account_type, balance_vnd, created_at
		FROM accounts
		ORDER BY account_code ASC
	`)
	if err != nil {
		return nil, dependency.Wrap(err)
	}
	defer rows.Close()

	list := make([]domain.Account, 0)
	for rows.Next() {
		var a domain.Account
		if err := rows.Scan(&a.AccountCode, &a.AccountName, &a.AccountType, &a.BalanceVND, &a.CreatedAt); err != nil {
			return nil, dependency.Wrap(err)
		}
		list = append(list, a)
	}
	return list, nil
}

// GetAccount returns a specific account
func (r *LedgerRepository) GetAccount(ctx context.Context, code string) (domain.Account, error) {
	if r.pool == nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		a, ok := r.accounts[code]
		if !ok {
			return domain.Account{}, ErrAccountNotFound
		}
		return *a, nil
	}

	var a domain.Account
	err := r.pool.QueryRow(ctx, `
		SELECT account_code, account_name, account_type, balance_vnd, created_at
		FROM accounts WHERE account_code = $1
	`, code).Scan(&a.AccountCode, &a.AccountName, &a.AccountType, &a.BalanceVND, &a.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Account{}, ErrAccountNotFound
		}
		return domain.Account{}, dependency.Wrap(err)
	}
	return a, nil
}
