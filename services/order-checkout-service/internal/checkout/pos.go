package checkout

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/contracts/events"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/outbox"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/orders"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/saga"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/snapshot"
)

const (
	PaymentMethodPOSCash = "POS_CASH"
	PaymentMethodPOSCard = "POS_CARD"
)

var (
	ErrInvalidSKU           = errors.New("unknown or unpriced SKU")
	ErrDuplicateSKU         = errors.New("duplicate SKU in POS sale items")
	ErrInsufficientCash     = errors.New("cash received is less than total amount")
	ErrInventoryUnavailable = errors.New("inventory service unavailable during POS sale")
	ErrInvalidPOSRequest    = errors.New("invalid POS sale request")
)

type SKUPricing struct {
	SkuID        string
	Name         string
	UnitPriceVND int64
}

type CatalogPricer interface {
	GetSKUPrice(ctx context.Context, skuID string) (SKUPricing, error)
}

type HandoverStock interface {
	Handover(ctx context.Context, orderID, warehouseID string) error
}

type POSItemInput struct {
	SkuID    string `json:"skuId"`
	Quantity int64  `json:"quantity"`
}

type POSSaleInput struct {
	CashierID       string
	IdempotencyKey  string
	WarehouseID     string
	PaymentMethod   string
	CustomerNote    string
	CashReceivedVND int64
	Items           []POSItemInput
}

type POSSaleItem struct {
	SkuID        string `json:"skuId"`
	Name         string `json:"name"`
	Quantity     int64  `json:"quantity"`
	UnitPriceVND int64  `json:"unitPriceVnd"`
	LineTotalVND int64  `json:"lineTotalVnd"`
}

type POSSaleReceipt struct {
	OrderID         string        `json:"orderId"`
	ReceiptNumber   string        `json:"receiptNumber"`
	Channel         string        `json:"channel"`
	WarehouseID     string        `json:"warehouseId"`
	CashierID       string        `json:"cashierId"`
	PaymentMethod   string        `json:"paymentMethod"`
	PaymentStatus   string        `json:"paymentStatus"`
	Status          string        `json:"status"`
	SubtotalVND     int64         `json:"subtotalVnd"`
	TotalVND        int64         `json:"totalVnd"`
	CashReceivedVND int64         `json:"cashReceivedVnd"`
	ChangeDueVND    int64         `json:"changeDueVnd"`
	CustomerNote    string        `json:"customerNote,omitempty"`
	Items           []POSSaleItem `json:"items"`
	CreatedAt       time.Time     `json:"createdAt"`
}

type POSSaleSummary struct {
	OrderID         string    `json:"orderId"`
	ReceiptNumber   string    `json:"receiptNumber"`
	Channel         string    `json:"channel"`
	WarehouseID     string    `json:"warehouseId"`
	CashierID       string    `json:"cashierId"`
	PaymentMethod   string    `json:"paymentMethod"`
	PaymentStatus   string    `json:"paymentStatus"`
	Status          string    `json:"status"`
	TotalVND        int64     `json:"totalVnd"`
	CashReceivedVND int64     `json:"cashReceivedVnd"`
	ChangeDueVND    int64     `json:"changeDueVnd"`
	ItemCount       int64     `json:"itemCount"`
	CreatedAt       time.Time `json:"createdAt"`
}

func randomPOSOrderID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return "ord-pos-" + hex.EncodeToString(b[:])
}

func formatReceiptNumber(warehouseID, orderID string) string {
	whShort := strings.ToUpper(strings.TrimSpace(warehouseID))
	parts := strings.Split(whShort, "-")
	if len(parts) >= 2 && parts[0] == "WH" && parts[1] != "" {
		whShort = parts[1]
	} else if len(whShort) > 6 {
		whShort = whShort[:6]
	}
	cleaned := strings.ToUpper(strings.ReplaceAll(strings.TrimPrefix(orderID, "ord-pos-"), "-", ""))
	if len(cleaned) > 8 {
		cleaned = cleaned[len(cleaned)-8:]
	}
	return fmt.Sprintf("POS-%s-%s", whShort, cleaned)
}

func posIntentHash(in POSSaleInput) string {
	var sb strings.Builder
	sb.WriteString("POS|")
	sb.WriteString(in.WarehouseID)
	sb.WriteByte('|')
	sb.WriteString(in.PaymentMethod)
	sb.WriteByte('|')
	sb.WriteString(fmt.Sprintf("%d", in.CashReceivedVND))
	sb.WriteByte('|')
	sb.WriteString(in.CustomerNote)
	for _, it := range in.Items {
		sb.WriteByte('|')
		sb.WriteString(it.SkuID)
		sb.WriteByte(':')
		sb.WriteString(fmt.Sprintf("%d", it.Quantity))
	}
	return orders.HashIntent(in.CashierID, sb.String())
}

func receiptFromOrder(o orders.Order) POSSaleReceipt {
	items := make([]POSSaleItem, len(o.Items))
	for i, it := range o.Items {
		items[i] = POSSaleItem{
			SkuID:        it.SkuID,
			Name:         it.SkuName,
			Quantity:     it.Quantity,
			UnitPriceVND: it.UnitPriceVND,
			LineTotalVND: it.UnitPriceVND * it.Quantity,
		}
	}
	channel := o.Channel
	if channel == "" {
		channel = orders.ChannelPOS
	}
	return POSSaleReceipt{
		OrderID:         o.OrderID,
		ReceiptNumber:   o.ReceiptNumber,
		Channel:         channel,
		WarehouseID:     o.WarehouseID,
		CashierID:       o.UserID,
		PaymentMethod:   o.PaymentMethod,
		PaymentStatus:   o.PaymentStatus,
		Status:          o.Status,
		SubtotalVND:     o.SubtotalVND,
		TotalVND:        o.TotalPayableVND,
		CashReceivedVND: o.CashReceivedVND,
		ChangeDueVND:    o.ChangeDueVND,
		CustomerNote:    o.CustomerNote,
		Items:           items,
		CreatedAt:       o.CreatedAt,
	}
}

func (s *Service) posSaleCompleted(o orders.Order) events.DomainEvent {
	return outbox.NewEvent(events.EventPOSSaleCompleted, aggregateOrder, o.OrderID, s.now(), map[string]any{
		"orderId":       o.OrderID,
		"receiptNumber": o.ReceiptNumber,
		"channel":       orders.ChannelPOS,
		"warehouseId":   o.WarehouseID,
		"cashierId":     o.UserID,
		"paymentMethod": o.PaymentMethod,
		"totalVnd":      o.TotalPayableVND,
	})
}

func (s *Service) CreatePOSSale(ctx context.Context, in POSSaleInput) (POSSaleReceipt, bool, error) {
	in.WarehouseID = strings.TrimSpace(in.WarehouseID)
	in.PaymentMethod = strings.ToUpper(strings.TrimSpace(in.PaymentMethod))
	in.CustomerNote = strings.TrimSpace(in.CustomerNote)

	if in.WarehouseID == "" || (in.PaymentMethod != PaymentMethodPOSCash && in.PaymentMethod != PaymentMethodPOSCard) || len(in.Items) == 0 || in.CashReceivedVND < 0 {
		return POSSaleReceipt{}, false, ErrInvalidPOSRequest
	}
	seen := make(map[string]struct{}, len(in.Items))
	normalizedItems := make([]POSItemInput, len(in.Items))
	for i, it := range in.Items {
		sku := strings.TrimSpace(it.SkuID)
		if sku == "" || it.Quantity < 1 {
			return POSSaleReceipt{}, false, ErrInvalidPOSRequest
		}
		if _, dup := seen[sku]; dup {
			return POSSaleReceipt{}, false, ErrDuplicateSKU
		}
		seen[sku] = struct{}{}
		normalizedItems[i] = POSItemInput{SkuID: sku, Quantity: it.Quantity}
	}
	in.Items = normalizedItems

	// Serialize concurrent attempts for the same cashier and Idempotency-Key in this instance before
	// checking or reserving stock, and claim the key in Orders.Create before mutating inventory.
	defer s.locks.lock("pos:" + in.CashierID + ":" + in.IdempotencyKey)()

	hash := posIntentHash(in)
	if existing, found, err := s.Orders.Lookup(ctx, in.CashierID, in.IdempotencyKey, hash); err != nil || found {
		if err != nil {
			return POSSaleReceipt{}, false, err
		}
		return s.posReplayResult(existing)
	}

	if s.Catalog == nil {
		return POSSaleReceipt{}, false, ErrInvalidSKU
	}

	orderItems := make([]orders.Item, len(in.Items))
	stockLines := make([]StockLine, len(in.Items))
	snapLines := make([]snapshot.Line, len(in.Items))
	var subtotalVND int64
	for i, it := range in.Items {
		priced, err := s.Catalog.GetSKUPrice(ctx, it.SkuID)
		if err != nil {
			return POSSaleReceipt{}, false, err
		}
		if priced.UnitPriceVND <= 0 {
			return POSSaleReceipt{}, false, ErrInvalidSKU
		}
		name := priced.Name
		if name == "" {
			name = it.SkuID
		}
		subtotalVND += priced.UnitPriceVND * it.Quantity
		orderItems[i] = orders.Item{
			SkuID:                it.SkuID,
			SkuName:              name,
			Quantity:             it.Quantity,
			UnitPriceVND:         priced.UnitPriceVND,
			LineDiscountVND:      0,
			AllocatedUnitPaidVND: priced.UnitPriceVND,
		}
		stockLines[i] = StockLine{SkuID: it.SkuID, Quantity: it.Quantity}
		snapLines[i] = snapshot.Line{
			SkuID:                it.SkuID,
			SkuName:              name,
			Quantity:             it.Quantity,
			UnitPriceVND:         priced.UnitPriceVND,
			LineDiscountVND:      0,
			AllocatedUnitPaidVND: priced.UnitPriceVND,
		}
	}
	totalVND := subtotalVND

	var cashReceivedVND, changeDueVND int64
	if in.PaymentMethod == PaymentMethodPOSCash {
		if in.CashReceivedVND > 0 {
			if in.CashReceivedVND < totalVND {
				return POSSaleReceipt{}, false, ErrInsufficientCash
			}
			cashReceivedVND = in.CashReceivedVND
			changeDueVND = in.CashReceivedVND - totalVND
		} else {
			cashReceivedVND = totalVND
			changeDueVND = 0
		}
	}

	orderID := s.newPOSID()
	receiptNumber := formatReceiptNumber(in.WarehouseID, orderID)

	built := orders.Order{
		OrderID:         orderID,
		UserID:          in.CashierID,
		Channel:         orders.ChannelPOS,
		ReceiptNumber:   receiptNumber,
		WarehouseID:     in.WarehouseID,
		Status:          orders.StatusPending,
		PaymentMethod:   in.PaymentMethod,
		PaymentStatus:   orders.PaymentUnpaid,
		SubtotalVND:     subtotalVND,
		DiscountVND:     0,
		ShippingFeeVND:  0,
		TotalPayableVND: totalVND,
		CapturedVND:     0,
		CashReceivedVND: cashReceivedVND,
		ChangeDueVND:    changeDueVND,
		CustomerNote:    in.CustomerNote,
		Items:           orderItems,
	}

	stored, replayed, err := s.Orders.Create(ctx, in.IdempotencyKey, hash, built)
	if err != nil {
		return POSSaleReceipt{}, false, err
	}
	if replayed {
		return s.posReplayResult(stored)
	}

	failPOS := func(failureCode string, retErr error) (POSSaleReceipt, bool, error) {
		if _, trErr := s.Orders.Transition(ctx, stored.OrderID, orders.StatusPending, string(saga.StateFailedClosed), orders.PaymentUnpaid, failureCode); trErr != nil {
			log.Printf("[pos] failed to mark POS order %s failed: %s", stored.OrderID, redact(trErr))
		}
		return POSSaleReceipt{}, false, retErr
	}

	if err := s.Stock.Reserve(ctx, stored.OrderID, in.WarehouseID, stockLines); err != nil {
		if errors.Is(err, ErrOutOfStock) {
			return failPOS(FailureOutOfStock, ErrOutOfStock)
		}
		s.releaseStock(ctx, stored.OrderID)
		return failPOS(FailureDependencyUnavailable, ErrInventoryUnavailable)
	}

	if err := s.Stock.Commit(ctx, stored.OrderID); err != nil {
		s.releaseStock(ctx, stored.OrderID)
		return failPOS(FailureDependencyUnavailable, ErrInventoryUnavailable)
	}

	handoverStock, ok := s.Stock.(HandoverStock)
	if !ok {
		if relErr := s.Stock.ReleaseCommitted(ctx, stored.OrderID); relErr != nil {
			log.Printf("[pos] rollback release-committed failed for %s: %v", stored.OrderID, relErr)
		}
		return failPOS(FailureDependencyUnavailable, ErrInventoryUnavailable)
	}
	if err := handoverStock.Handover(ctx, stored.OrderID, in.WarehouseID); err != nil {
		if relErr := s.Stock.ReleaseCommitted(ctx, stored.OrderID); relErr != nil {
			log.Printf("[pos] rollback release-committed failed for %s: %v", stored.OrderID, relErr)
		}
		return failPOS(FailureDependencyUnavailable, ErrInventoryUnavailable)
	}

	if err := s.Orders.RecordCapture(ctx, stored.OrderID, totalVND, 0); err != nil {
		log.Printf("[pos] failed to record capture for %s: %s", stored.OrderID, redact(err))
		return POSSaleReceipt{}, false, err
	}

	completed, err := s.Orders.Transition(ctx, stored.OrderID, orders.StatusPending, orders.StatusDeliveryComplete, orders.PaymentCollected, "", s.posSaleCompleted(stored))
	if err != nil {
		log.Printf("[pos] failed to complete POS order %s: %s", stored.OrderID, redact(err))
		return POSSaleReceipt{}, false, err
	}

	if s.Snapshots != nil {
		snap := snapshot.Snapshot{
			OrderID:         completed.OrderID,
			UserID:          completed.UserID,
			Channel:         orders.ChannelPOS,
			WarehouseID:     completed.WarehouseID,
			ReceiptNumber:   completed.ReceiptNumber,
			Lines:           snapLines,
			SubtotalVND:     completed.SubtotalVND,
			DiscountVND:     0,
			ShippingFeeVND:  0,
			TotalPayableVND: completed.TotalPayableVND,
			CreatedAt:       completed.CreatedAt,
		}
		if snapErr := s.Snapshots.Save(ctx, snap); snapErr != nil {
			log.Printf("[pos] failed to save commercial snapshot for %s: %s", completed.OrderID, redact(snapErr))
		}
	}

	return receiptFromOrder(completed), false, nil
}

func (s *Service) posReplayResult(o orders.Order) (POSSaleReceipt, bool, error) {
	switch o.Status {
	case orders.StatusDeliveryComplete:
		return receiptFromOrder(o), true, nil
	case string(saga.StateFailedClosed):
		if o.FailureCode == FailureOutOfStock {
			return POSSaleReceipt{}, false, ErrOutOfStock
		}
		return POSSaleReceipt{}, false, ErrInventoryUnavailable
	default:
		return POSSaleReceipt{}, false, ErrInventoryUnavailable
	}
}

func (s *Service) ListPOSSales(ctx context.Context, warehouseIDs []string, limit int) ([]POSSaleSummary, int, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > orders.MaxLimit {
		limit = orders.MaxLimit
	}
	list, total, err := s.Orders.Query(ctx, orders.Filter{
		Channel:      orders.ChannelPOS,
		Statuses:     []string{orders.StatusDeliveryComplete},
		WarehouseIDs: warehouseIDs,
		Limit:        limit,
	})
	if err != nil {
		return nil, 0, err
	}
	out := make([]POSSaleSummary, len(list))
	for i, o := range list {
		out[i] = POSSaleSummary{
			OrderID:         o.OrderID,
			ReceiptNumber:   o.ReceiptNumber,
			Channel:         o.Channel,
			WarehouseID:     o.WarehouseID,
			CashierID:       o.UserID,
			PaymentMethod:   o.PaymentMethod,
			PaymentStatus:   o.PaymentStatus,
			Status:          o.Status,
			TotalVND:        o.TotalPayableVND,
			CashReceivedVND: o.CashReceivedVND,
			ChangeDueVND:    o.ChangeDueVND,
			ItemCount:       o.ItemCount,
			CreatedAt:       o.CreatedAt,
		}
	}
	return out, total, nil
}
