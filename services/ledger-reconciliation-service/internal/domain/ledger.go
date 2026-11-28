package domain

import (
	"errors"
	"fmt"
	"time"
)

var (
	ErrUnbalancedEntry   = errors.New("journal entry is unbalanced: sum of debits must equal sum of credits")
	ErrInsufficientLines = errors.New("journal entry must have at least two posting lines")
	ErrInvalidPosting    = errors.New("invalid posting line: account code and positive amount are required")
	ErrMissingField      = errors.New("required entry field is missing")
)

type AccountType string

const (
	AccountAsset     AccountType = "ASSET"
	AccountLiability AccountType = "LIABILITY"
	AccountEquity    AccountType = "EQUITY"
	AccountRevenue   AccountType = "REVENUE"
	AccountExpense   AccountType = "EXPENSE"
)

type Account struct {
	AccountCode string      `json:"accountCode"`
	AccountName string      `json:"accountName"`
	AccountType AccountType `json:"accountType"`
	BalanceVND  int64       `json:"balanceVnd"`
	CreatedAt   time.Time   `json:"createdAt"`
}

type PostingDirection string

const (
	DirectionDebit  PostingDirection = "DEBIT"
	DirectionCredit PostingDirection = "CREDIT"
)

type Posting struct {
	PostingID   int64            `json:"postingId,omitempty"`
	EntryID     string           `json:"entryId,omitempty"`
	AccountCode string           `json:"accountCode"`
	Direction   PostingDirection `json:"direction"`
	AmountVND   int64            `json:"amountVnd"`
	CreatedAt   time.Time        `json:"createdAt,omitempty"`
}

type JournalEntry struct {
	EntryID     string    `json:"entryId"`
	EventType   string    `json:"eventType"`
	ReferenceID string    `json:"referenceId"`
	Description string    `json:"description"`
	PostedAt    time.Time `json:"postedAt"`
	CreatedBy   string    `json:"createdBy"`
	CreatedAt   time.Time `json:"createdAt"`
	Postings    []Posting `json:"postings"`
}

// ValidateJournalEntry enforces the fundamental double-entry invariant: sum(debit) == sum(credit) > 0
func ValidateJournalEntry(entry JournalEntry) error {
	if entry.EntryID == "" || entry.EventType == "" || entry.ReferenceID == "" {
		return ErrMissingField
	}
	if len(entry.Postings) < 2 {
		return ErrInsufficientLines
	}

	var sumDebit int64
	var sumCredit int64

	for _, p := range entry.Postings {
		if p.AccountCode == "" || p.AmountVND <= 0 {
			return ErrInvalidPosting
		}
		switch p.Direction {
		case DirectionDebit:
			sumDebit += p.AmountVND
		case DirectionCredit:
			sumCredit += p.AmountVND
		default:
			return fmt.Errorf("invalid posting direction: %s", p.Direction)
		}
	}

	if sumDebit == 0 || sumDebit != sumCredit {
		return fmt.Errorf("%w: total debit=%d, total credit=%d", ErrUnbalancedEntry, sumDebit, sumCredit)
	}

	return nil
}

// NewPaymentCapturedEntry builds a balanced double entry for an order payment collection
// Debit: 1120 (Gateway Receivable), Credit: 5110 (Gross Sales Revenue)
func NewPaymentCapturedEntry(entryID, orderID string, amountVND int64, actor string) (JournalEntry, error) {
	if amountVND <= 0 {
		return JournalEntry{}, fmt.Errorf("captured amount must be positive, got %d", amountVND)
	}
	now := time.Now().UTC()
	entry := JournalEntry{
		EntryID:     entryID,
		EventType:   "PAYMENT_CAPTURED",
		ReferenceID: orderID,
		Description: fmt.Sprintf("Payment captured for order %s", orderID),
		PostedAt:    now,
		CreatedBy:   actor,
		CreatedAt:   now,
		Postings: []Posting{
			{
				AccountCode: "1120",
				Direction:   DirectionDebit,
				AmountVND:   amountVND,
			},
			{
				AccountCode: "5110",
				Direction:   DirectionCredit,
				AmountVND:   amountVND,
			},
		},
	}
	if err := ValidateJournalEntry(entry); err != nil {
		return JournalEntry{}, err
	}
	return entry, nil
}

// NewRefundIssuedEntry builds a balanced double entry for a customer refund disbursement
// Debit: 5210 (Sales Returns & Deductions), Credit: 1120 (Gateway Receivable)
func NewRefundIssuedEntry(entryID, orderID, refundID string, amountVND int64, actor string) (JournalEntry, error) {
	if amountVND <= 0 {
		return JournalEntry{}, fmt.Errorf("refund amount must be positive, got %d", amountVND)
	}
	now := time.Now().UTC()
	entry := JournalEntry{
		EntryID:     entryID,
		EventType:   "REFUND_ISSUED",
		ReferenceID: refundID,
		Description: fmt.Sprintf("Refund issued for order %s (refund %s)", orderID, refundID),
		PostedAt:    now,
		CreatedBy:   actor,
		CreatedAt:   now,
		Postings: []Posting{
			{
				AccountCode: "5210",
				Direction:   DirectionDebit,
				AmountVND:   amountVND,
			},
			{
				AccountCode: "1120",
				Direction:   DirectionCredit,
				AmountVND:   amountVND,
			},
		},
	}
	if err := ValidateJournalEntry(entry); err != nil {
		return JournalEntry{}, err
	}
	return entry, nil
}

// NewSettlementMatchedEntry builds a balanced double entry for a settlement payout
// Debit: 1110 (Bank Account: net payout)
// Debit: 6410 (Processing Fees: fee amount)
// Optional Debit: 1388 (Discrepancy Receivable: shortfall to be investigated)
// Credit: 1120 (Gateway Receivable: gross captured amount)
func NewSettlementMatchedEntry(
	entryID, orderID, batchID string,
	grossVND, feeVND, netVND, diffVND int64,
	actor string,
) (JournalEntry, error) {
	if grossVND <= 0 {
		return JournalEntry{}, fmt.Errorf("gross amount must be positive, got %d", grossVND)
	}
	now := time.Now().UTC()

	postings := settlementPostings(grossVND, feeVND, netVND, diffVND, "6410", "1120")
	entry := JournalEntry{
		EntryID:     entryID,
		EventType:   "SETTLEMENT_MATCHED",
		ReferenceID: fmt.Sprintf("%s:%s", batchID, orderID),
		Description: fmt.Sprintf("Settlement payout matched for order %s (batch %s)", orderID, batchID),
		PostedAt:    now,
		CreatedBy:   actor,
		CreatedAt:   now,
		Postings:    postings,
	}

	if err := ValidateJournalEntry(entry); err != nil {
		return JournalEntry{}, err
	}
	return entry, nil
}

func settlementPostings(gross, fee, net, difference int64, feeAccount, receivableAccount string) []Posting {
	postings := make([]Posting, 0, 4)
	if net != 0 {
		postings = append(postings, Posting{AccountCode: "1110", Direction: DirectionDebit, AmountVND: net})
	}
	if fee != 0 {
		postings = append(postings, Posting{AccountCode: feeAccount, Direction: DirectionDebit, AmountVND: fee})
	}
	if difference > 0 {
		postings = append(postings, Posting{AccountCode: "1388", Direction: DirectionDebit, AmountVND: difference})
	} else if difference < 0 {
		postings = append(postings, Posting{AccountCode: "3389", Direction: DirectionCredit, AmountVND: -difference})
	}
	return append(postings, Posting{AccountCode: receivableAccount, Direction: DirectionCredit, AmountVND: gross})
}

// NewCompensatingAdjustmentEntry builds a balanced double entry to offset an investigated discrepancy from account 1388
// Debit: compensatingAccountCode (default 6410 Payment Gateway Processing Fees)
// Credit: 1388 (Reconciliation Discrepancies Receivable)
func NewCompensatingAdjustmentEntry(
	entryID, caseID, orderID string,
	discrepancyVND int64,
	compensatingAccountCode string,
	category, notes string,
	actor string,
) (JournalEntry, error) {
	if discrepancyVND == 0 {
		return JournalEntry{}, errors.New("discrepancy amount must be nonzero")
	}
	debitAccount, creditAccount := compensatingAccountCode, "1388"
	if compensatingAccountCode == "" {
		debitAccount = "6410"
	}
	if discrepancyVND < 0 {
		debitAccount, creditAccount, discrepancyVND = "3389", debitAccount, -discrepancyVND
	}
	now := time.Now().UTC()
	entry := JournalEntry{
		EntryID:     entryID,
		EventType:   "DISCREPANCY_RESOLVED",
		ReferenceID: caseID,
		Description: fmt.Sprintf("Discrepancy resolved for case %s (order %s, %s): %s", caseID, orderID, category, notes),
		PostedAt:    now,
		CreatedBy:   actor,
		CreatedAt:   now,
		Postings: []Posting{
			{
				AccountCode: debitAccount,
				Direction:   DirectionDebit,
				AmountVND:   discrepancyVND,
			},
			{
				AccountCode: creditAccount,
				Direction:   DirectionCredit,
				AmountVND:   discrepancyVND,
			},
		},
	}
	if err := ValidateJournalEntry(entry); err != nil {
		return JournalEntry{}, err
	}
	return entry, nil
}

// NewCODDeliveryConfirmedEntry builds a balanced double entry when a COD order is delivered and cash is collected by the carrier
// Debit: 1310 (3PL COD Receivables: carrier has collected cash from customer)
// Credit: 5110 (Gross Sales Revenue)
func NewCODDeliveryConfirmedEntry(entryID, orderID, carrierID string, amountVND int64, actor string) (JournalEntry, error) {
	if amountVND <= 0 {
		return JournalEntry{}, fmt.Errorf("collected amount must be positive, got %d", amountVND)
	}
	if carrierID == "" {
		carrierID = "DEFAULT_CARRIER"
	}
	now := time.Now().UTC()
	entry := JournalEntry{
		EntryID:     entryID,
		EventType:   "COD_DELIVERY_CONFIRMED",
		ReferenceID: orderID,
		Description: fmt.Sprintf("COD delivery confirmed and cash collected for order %s via carrier %s", orderID, carrierID),
		PostedAt:    now,
		CreatedBy:   actor,
		CreatedAt:   now,
		Postings: []Posting{
			{
				AccountCode: "1310",
				Direction:   DirectionDebit,
				AmountVND:   amountVND,
			},
			{
				AccountCode: "5110",
				Direction:   DirectionCredit,
				AmountVND:   amountVND,
			},
		},
	}
	if err := ValidateJournalEntry(entry); err != nil {
		return JournalEntry{}, err
	}
	return entry, nil
}

// NewCODSettlementMatchedEntry builds a balanced double entry for a carrier COD cash settlement payout
// Debit: 1110 (Cash in Bank: net cash remitted by carrier = collected - fee)
// Debit: 6420 (Carrier COD Processing Fees)
// Optional Debit: 1388 (Discrepancy Receivable: shortfall to be investigated)
// Credit: 1310 (3PL COD Receivables: gross collected amount)
func NewCODSettlementMatchedEntry(
	entryID, orderID, batchID, carrierID string,
	grossCollectedVND, feeVND, netPayoutVND, diffVND int64,
	actor string,
) (JournalEntry, error) {
	if grossCollectedVND <= 0 {
		return JournalEntry{}, fmt.Errorf("gross collected amount must be positive, got %d", grossCollectedVND)
	}
	now := time.Now().UTC()
	postings := settlementPostings(grossCollectedVND, feeVND, netPayoutVND, diffVND, "6420", "1310")
	entry := JournalEntry{
		EntryID:     entryID,
		EventType:   "COD_SETTLEMENT_MATCHED",
		ReferenceID: fmt.Sprintf("%s:%s", batchID, orderID),
		Description: fmt.Sprintf("Carrier COD settlement payout matched for order %s via carrier %s (batch %s)", orderID, carrierID, batchID),
		PostedAt:    now,
		CreatedBy:   actor,
		CreatedAt:   now,
		Postings:    postings,
	}
	if err := ValidateJournalEntry(entry); err != nil {
		return JournalEntry{}, err
	}
	return entry, nil
}

func NewPOSSaleCompletedEntry(entryID, orderID, warehouseID, paymentMethod string, amountVND int64, actor string) (JournalEntry, error) {
	if amountVND <= 0 {
		return JournalEntry{}, fmt.Errorf("POS sale amount must be positive, got %d", amountVND)
	}
	var debitAccount string
	switch paymentMethod {
	case "POS_CASH":
		debitAccount = "1111"
	case "POS_CARD":
		debitAccount = "1120"
	default:
		return JournalEntry{}, fmt.Errorf("unsupported POS payment method: %s", paymentMethod)
	}
	if actor == "" {
		actor = "pos-consumer"
	}
	now := time.Now().UTC()
	entry := JournalEntry{
		EntryID:     entryID,
		EventType:   "POS_SALE_COMPLETED",
		ReferenceID: orderID,
		Description: fmt.Sprintf("POS sale completed for order %s at %s (%s)", orderID, warehouseID, paymentMethod),
		PostedAt:    now,
		CreatedBy:   actor,
		CreatedAt:   now,
		Postings: []Posting{
			{
				AccountCode: debitAccount,
				Direction:   DirectionDebit,
				AmountVND:   amountVND,
			},
			{
				AccountCode: "5110",
				Direction:   DirectionCredit,
				AmountVND:   amountVND,
			},
		},
	}
	if err := ValidateJournalEntry(entry); err != nil {
		return JournalEntry{}, err
	}
	return entry, nil
}
