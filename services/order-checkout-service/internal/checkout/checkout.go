// Package checkout places an order: it checks the accepted quote, records the order under the customer's
// Idempotency-Key, reserves the voucher and the stock, creates the payment attempt and, when a step fails,
// undoes the earlier ones in reverse order (B03, I02, P03, P12). The saga state machine decides states and
// effects; this package only performs them.
package checkout

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/contracts/ids"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/orders"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/saga"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/snapshot"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/voucher"
)

// ReservationTTL is how long the customer has to pay (P03). It mirrors the inventory reservation TTL.
const ReservationTTL = 10 * time.Minute

// Failure codes stored on a closed order. They are also the API error codes of the place-order call.
const (
	FailureOutOfStock            = "OUT_OF_STOCK"
	FailureDependencyUnavailable = "DEPENDENCY_UNAVAILABLE"
	FailurePaymentFailed         = "PAYMENT_FAILED"
	FailureReservationExpired    = "RESERVATION_EXPIRED"
	FailureUnknownExpired        = "PAYMENT_UNKNOWN_EXPIRED"
	FailureStockLost             = "STOCK_RESERVATION_LOST"
	FailurePlacementInterrupted  = "PLACEMENT_INTERRUPTED"
)

// PayRefundPending is the payment status of an order that is closed while the customer's money is held: it is
// owed back (SC05).
const PayRefundPending = saga.PayRefundPending

var (
	// ErrQuoteNotFound: the quote does not exist or belongs to someone else.
	ErrQuoteNotFound = errors.New("quote not found")
	// ErrQuoteExpired: the quote's five minutes are over (P02); the customer must accept a new one.
	ErrQuoteExpired = errors.New("quote has expired")
	// ErrQuoteChanged: a price changed or a SKU stopped being orderable since the quote; the customer must accept a new one.
	ErrQuoteChanged = errors.New("quote no longer matches the catalog")
	// ErrAddressNotFound: the delivery address of the quote is not in the customer's address book any more.
	ErrAddressNotFound = errors.New("delivery address not found")
	// ErrQuoteInvalid: the stored quote is internally inconsistent, so no money amount can be trusted.
	ErrQuoteInvalid = errors.New("quote is not usable")
	// ErrReservationLost: the stock reservation of the order is gone (expired, released or never made).
	ErrReservationLost = errors.New("stock reservation is not available")
	// ErrOutOfStock: the warehouse cannot cover the whole order (SC03).
	ErrOutOfStock = errors.New("insufficient stock")
)

// QuoteLine is one priced line of an accepted quote (whole VND).
type QuoteLine struct {
	SkuID, SkuName                                                string
	Quantity, UnitPriceVND, LineDiscountVND, AllocatedUnitPaidVND int64
}

// Quote is the quote the customer accepted, as stored by the catalog service.
type Quote struct {
	ID, UserID, AddressID, VoucherCode                        string
	SubtotalVND, DiscountVND, ShippingFeeVND, TotalPayableVND int64
	Lines                                                     []QuoteLine
	ExpiresAt                                                 time.Time
	// Stale: the catalog says a price changed or a SKU is no longer orderable since the quote was made.
	Stale bool
}

// StockLine is one line of a reservation.
type StockLine struct {
	SkuID    string
	Quantity int64
}

// Attempt is the payment attempt created for an order.
type Attempt struct{ AttemptID, Status string }

// Quotes reads a quote with the caller's own token, so the catalog applies its owner check.
type Quotes interface {
	Get(ctx context.Context, bearer, quoteID string) (Quote, error)
}

// Stock reserves and releases the stock of a whole order. Reserve returns ErrOutOfStock when it cannot cover it.
// Release is idempotent and succeeds when nothing is held.
type Stock interface {
	Reserve(ctx context.Context, orderID, warehouseID string, lines []StockLine) error
	Release(ctx context.Context, orderID string) error
	// Commit turns the reservation into committed stock (payment captured); ErrReservationLost when it cannot.
	Commit(ctx context.Context, orderID string) error
	// ExceptionHold extends the reservation to the 30-minute exception hold; ErrReservationLost when it is gone.
	ExceptionHold(ctx context.Context, orderID string) error
	// ReleaseCommitted gives committed (paid, not handed over) stock back to available. Idempotent;
	// ErrStockHandedOver when the stock already left the warehouse.
	ReleaseCommitted(ctx context.Context, orderID string) error
}

// PaymentState is the payment service's view of an order's attempt.
type PaymentState struct {
	AttemptID   string
	Status      string
	CapturedVND int64
}

// Payments creates the single payment attempt of an order.
type Payments interface {
	CreateAttempt(ctx context.Context, orderID, userID string, amountVND int64) (Attempt, error)
	// AttemptState reads where the order's payment stands; found=false when the payment service has no attempt.
	AttemptState(ctx context.Context, orderID string) (state PaymentState, found bool, err error)
	// CloseAttempt tells the payment service the order is closed: money that arrives later is refunded, and
	// money already captured is owed back. Idempotent; an unknown attempt counts as closed.
	CloseAttempt(ctx context.Context, attemptID string) error
}

// Addresses reads the customer's delivery address with their own token, so IAM applies its owner check.
// ErrAddressNotFound when it does not exist. The address is personal data: it goes into the snapshot only.
type Addresses interface {
	Get(ctx context.Context, bearer, addressID string) (snapshot.Address, error)
}

// Carts removes what the customer just bought from their cart (best effort, idempotent).
type Carts interface {
	RemoveOrdered(ctx context.Context, userID string, skuIDs []string) error
}

// Deps are the collaborators of a Service.
type Deps struct {
	Orders      orders.Store
	Quotes      Quotes
	Stock       Stock
	Payments    Payments
	Vouchers    voucher.Reserver
	Carts       Carts // optional
	Addresses   Addresses
	Snapshots   snapshot.Store
	Fulfillment Fulfillment // asked to stop the parcel of a paid order that is cancelled (B05)
	WarehouseID string
}

// Principal is the verified caller: identity and token both come from the access token, never from the body.
type Principal struct{ UserID, Bearer string }

// Service places orders.
type Service struct {
	Deps
	locks orderLocks
	now   func() time.Time
	newID func() string
}

func New(d Deps) *Service {
	return &Service{Deps: d, now: func() time.Time { return time.Now().UTC() }, newID: randomOrderID}
}

// WithClock replaces the clock (tests).
func (s *Service) WithClock(now func() time.Time) *Service { s.now = now; return s }

// WithIDs replaces the order id generator (tests).
func (s *Service) WithIDs(newID func() string) *Service { s.newID = newID; return s }

func randomOrderID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // the OS random source failing is not recoverable
	}
	return "O-" + hex.EncodeToString(b[:])
}

// Place records and runs the order for (caller, key, quoteID). It returns replayed=true when the key already
// produced an order (the stored order is returned and nothing runs again, even if the quote has expired since).
// A step that fails closes the order: the returned order then has Status FAILED_CLOSED and a FailureCode, and
// the error is nil. Errors are for calls that created no order (bad quote, conflict, store outage).
func (s *Service) Place(ctx context.Context, p Principal, key, quoteID string) (orders.Order, bool, error) {
	hash := orders.HashIntent(p.UserID, quoteID)
	if existing, found, err := s.Orders.Lookup(ctx, p.UserID, key, hash); err != nil || found {
		return existing, found, err
	}
	quote, err := s.Quotes.Get(ctx, p.Bearer, quoteID)
	if err != nil {
		return orders.Order{}, false, err
	}
	if err := s.checkQuote(quote, p.UserID); err != nil {
		return orders.Order{}, false, err
	}
	address, err := s.Addresses.Get(ctx, p.Bearer, quote.AddressID)
	if err != nil {
		return orders.Order{}, false, err
	}
	expires := s.now().Add(ReservationTTL)
	built := s.buildOrder(quote, expires)
	stored, replayed, err := s.Orders.Create(ctx, key, hash, built, s.orderCreated(built))
	if err != nil || replayed {
		return stored, replayed, err
	}
	// The frozen snapshot (I04) exists before anything is reserved: without it the order is closed.
	if err := s.Snapshots.Save(ctx, buildSnapshot(quote, stored, address)); err != nil {
		log.Printf("[order] cannot store the snapshot of order %s: %s", stored.OrderID, redact(err))
		return s.close(ctx, stored, saga.StatePending, saga.ReserveFailed{}, FailureDependencyUnavailable), false, nil
	}
	return s.run(ctx, p, stored), false, nil
}

func buildSnapshot(q Quote, o orders.Order, address snapshot.Address) snapshot.Snapshot {
	lines := make([]snapshot.Line, len(q.Lines))
	for i, l := range q.Lines {
		lines[i] = snapshot.Line{SkuID: l.SkuID, SkuName: l.SkuName, Quantity: l.Quantity, UnitPriceVND: l.UnitPriceVND,
			LineDiscountVND: l.LineDiscountVND, AllocatedUnitPaidVND: l.AllocatedUnitPaidVND}
	}
	return snapshot.Snapshot{
		OrderID: o.OrderID, UserID: o.UserID, QuoteID: q.ID, VoucherCode: q.VoucherCode, Lines: lines,
		SubtotalVND: q.SubtotalVND, DiscountVND: q.DiscountVND, ShippingFeeVND: q.ShippingFeeVND, TotalPayableVND: q.TotalPayableVND,
		ShippingAddress: address, CreatedAt: o.CreatedAt,
	}
}

func (s *Service) checkQuote(q Quote, userID string) error {
	if q.UserID != userID {
		return ErrQuoteNotFound
	}
	if !s.now().Before(q.ExpiresAt) {
		return ErrQuoteExpired
	}
	if q.Stale {
		return ErrQuoteChanged
	}
	if len(q.Lines) == 0 || q.AddressID == "" || q.TotalPayableVND <= 0 ||
		q.SubtotalVND-q.DiscountVND+q.ShippingFeeVND != q.TotalPayableVND {
		return ErrQuoteInvalid
	}
	for _, l := range q.Lines {
		if l.SkuID == "" || l.Quantity <= 0 || l.UnitPriceVND < 0 {
			return ErrQuoteInvalid
		}
	}
	return nil
}

func (s *Service) buildOrder(q Quote, expires time.Time) orders.Order {
	items := make([]orders.Item, len(q.Lines))
	for i, l := range q.Lines {
		items[i] = orders.Item{SkuID: l.SkuID, SkuName: l.SkuName, Quantity: l.Quantity, UnitPriceVND: l.UnitPriceVND,
			LineDiscountVND: l.LineDiscountVND, AllocatedUnitPaidVND: l.AllocatedUnitPaidVND}
	}
	return orders.Order{
		OrderID: s.newID(), UserID: q.UserID, QuoteID: q.ID, AddressID: q.AddressID, WarehouseID: s.WarehouseID, VoucherCode: q.VoucherCode,
		Status: orders.StatusPending, PaymentStatus: orders.PaymentUnpaid,
		SubtotalVND: q.SubtotalVND, DiscountVND: q.DiscountVND, ShippingFeeVND: q.ShippingFeeVND, TotalPayableVND: q.TotalPayableVND,
		Items: items, ExpiresAt: &expires,
	}
}

// run takes the voucher, then the stock, then creates the payment attempt.
func (s *Service) run(ctx context.Context, p Principal, o orders.Order) orders.Order {
	if o.VoucherCode != "" {
		if err := s.Vouchers.Reserve(ctx, o.VoucherCode, p.UserID, o.OrderID, s.now()); err != nil {
			return s.close(ctx, o, saga.StatePending, saga.ReserveFailed{}, failureOf(err))
		}
	}

	lines := make([]StockLine, len(o.Items))
	for i, it := range o.Items {
		lines[i] = StockLine{SkuID: it.SkuID, Quantity: it.Quantity}
	}
	if err := s.Stock.Reserve(ctx, o.OrderID, o.WarehouseID, lines); err != nil {
		if !errors.Is(err, ErrOutOfStock) {
			// A timeout does not say whether the stock was taken: release it to be safe (idempotent).
			s.releaseStock(ctx, o.OrderID)
		}
		return s.close(ctx, o, saga.StatePending, saga.ReserveFailed{}, failureOf(err))
	}
	reserved, err := s.transition(ctx, o, saga.StatePending, saga.Reserved{}, "")
	if err != nil {
		return s.undoAfterStoreFailure(ctx, o)
	}

	if _, err := s.Payments.CreateAttempt(ctx, reserved.OrderID, p.UserID, reserved.TotalPayableVND); err != nil {
		return s.close(ctx, reserved, saga.StateReserved, saga.PaymentFailed{}, failureOf(err))
	}
	return reserved
}

// failureOf maps a step error to the failure code stored on the order.
func failureOf(err error) string {
	var refusal *voucher.Refusal
	switch {
	case errors.As(err, &refusal):
		return refusal.Reason
	case errors.Is(err, ErrOutOfStock):
		return FailureOutOfStock
	}
	return FailureDependencyUnavailable
}

// close applies the saga's failure event: it runs the release effects first (they are idempotent, so a crash
// before the state is stored is repaired by running them again) and then stores the closed state.
func (s *Service) close(ctx context.Context, o orders.Order, from saga.State, ev saga.Event, failureCode string) orders.Order {
	res, err := saga.Next(from, ev)
	if err != nil {
		log.Printf("[order] saga transition rejected for order %s: %v", o.OrderID, err)
		return o
	}
	s.apply(ctx, o, res.Effects)
	if from == saga.StateReserved {
		// The attempt may exist even if its creation call failed on the way back: close it so a payment that
		// still arrives is refunded instead of landing on a closed order.
		if err := s.Payments.CloseAttempt(ctx, ids.PaymentAttemptID(o.OrderID)); err != nil {
			log.Printf("[order] payment attempt close failed for order %s (the sweeper repeats it): %s", o.OrderID, redact(err))
		}
	}
	closed, err := s.Orders.Transition(ctx, o.OrderID, string(from), string(res.State), res.PaymentStatus, failureCode,
		s.orderFailed(o, failureCode, res.PaymentStatus))
	if err != nil {
		log.Printf("[order] cannot store the closed state of order %s: %v", o.OrderID, err)
		o.Status, o.PaymentStatus, o.FailureCode = string(res.State), res.PaymentStatus, failureCode
		return o
	}
	return closed
}

func (s *Service) transition(ctx context.Context, o orders.Order, from saga.State, ev saga.Event, failureCode string) (orders.Order, error) {
	res, err := saga.Next(from, ev)
	if err != nil {
		return orders.Order{}, err
	}
	return s.Orders.Transition(ctx, o.OrderID, string(from), string(res.State), res.PaymentStatus, failureCode)
}

// undoAfterStoreFailure releases what was taken when the new state could not be stored.
func (s *Service) undoAfterStoreFailure(ctx context.Context, o orders.Order) orders.Order {
	s.releaseStock(ctx, o.OrderID)
	s.releaseVoucher(ctx, o)
	o.FailureCode = FailureDependencyUnavailable
	return o
}

// apply runs the release effects of a failed placement. The effects of a payment outcome (commit, consume,
// exception hold, refund) are run by ApplyPayment.
func (s *Service) apply(ctx context.Context, o orders.Order, effects []saga.Effect) {
	for _, e := range effects {
		switch e {
		case saga.ReleaseStock:
			s.releaseStock(ctx, o.OrderID)
		case saga.ReleaseVoucher:
			s.releaseVoucher(ctx, o)
		}
	}
}

func (s *Service) releaseStock(ctx context.Context, orderID string) {
	if err := s.Stock.Release(ctx, orderID); err != nil {
		// The reservation also expires by itself after its TTL, so a failed release is logged, not fatal.
		log.Printf("[order] stock release failed for order %s (the reservation expires on its own): %v", orderID, redact(err))
	}
}

func (s *Service) releaseVoucher(ctx context.Context, o orders.Order) {
	if o.VoucherCode == "" {
		return
	}
	if err := s.Vouchers.Release(ctx, o.VoucherCode, o.OrderID); err != nil {
		log.Printf("[order] voucher release failed for order %s: %v", o.OrderID, redact(err))
	}
}

// redact keeps log lines free of anything but the error class: peers' messages are not logged verbatim.
func redact(err error) string {
	if dependency.IsUnavailable(err) {
		return "dependency unavailable"
	}
	return "unexpected error"
}
