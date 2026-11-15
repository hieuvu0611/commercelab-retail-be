// Package cart is the signed-in customer's cart. It stores only SKU ids and quantities; every price and name is
// read from the catalog when the cart is viewed, and nothing is reserved (P02).
package cart

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/repository"
)

const (
	MinQuantity int64 = 1
	MaxQuantity int64 = 99
	// MaxLines bounds the distinct SKUs in one cart.
	MaxLines = 50
	// TTL is how long an untouched cart is kept (D-05); every write renews it.
	TTL = 30 * 24 * time.Hour
)

var (
	ErrInvalidQuantity = errors.New("quantity must be between 1 and 99")
	ErrCartFull        = errors.New("the cart holds at most 50 different items")
)

// Store keeps SKU quantities per user.
type Store interface {
	Get(ctx context.Context, userID string) (map[string]int64, error)
	// SetMany writes the quantities and renews the TTL.
	SetMany(ctx context.Context, userID string, lines map[string]int64) error
	Remove(ctx context.Context, userID, skuID string) error
}

// Pricer reads the price in effect of an orderable SKU.
type Pricer interface {
	GetSkuPrice(ctx context.Context, skuID string) (repository.SkuPrice, error)
}

type Line struct {
	SkuID    string `json:"skuId"`
	Quantity int64  `json:"quantity"`
}

// Item is one priced cart line. A SKU that is no longer orderable stays visible with Orderable false, no price
// and no contribution to the subtotal.
type Item struct {
	SkuID        string `json:"skuId"`
	SkuName      string `json:"skuName"`
	UnitPriceVND int64  `json:"unitPriceVnd"`
	Quantity     int64  `json:"quantity"`
	LineTotalVND int64  `json:"lineTotalVnd"`
	Orderable    bool   `json:"orderable"`
}

type View struct {
	Items       []Item `json:"items"`
	ItemCount   int64  `json:"itemCount"`
	SubtotalVND int64  `json:"subtotalVnd"`
}

// Skipped is a guest line Merge did not take, with the error code that explains why.
type Skipped struct {
	SkuID  string `json:"skuId"`
	Reason string `json:"reason"`
}

type Service struct {
	store  Store
	pricer Pricer
}

func NewService(store Store, pricer Pricer) *Service {
	return &Service{store: store, pricer: pricer}
}

// View returns the cart priced with the current catalog prices, ordered by SKU id.
func (s *Service) View(ctx context.Context, userID string) (View, error) {
	lines, err := s.store.Get(ctx, userID)
	if err != nil {
		return View{}, err
	}
	ids := make([]string, 0, len(lines))
	for id := range lines {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	view := View{Items: make([]Item, 0, len(ids))}
	for _, id := range ids {
		item := Item{SkuID: id, Quantity: lines[id]}
		sp, err := s.pricer.GetSkuPrice(ctx, id)
		switch {
		case err == nil:
			item.SkuName, item.UnitPriceVND, item.Orderable = sp.SkuName, sp.UnitPriceVND, true
			item.LineTotalVND = sp.UnitPriceVND * item.Quantity
			view.SubtotalVND += item.LineTotalVND
		case errors.Is(err, repository.ErrSkuNotFound), errors.Is(err, repository.ErrSkuNotOrderable):
		default:
			return View{}, err
		}
		view.ItemCount += item.Quantity
		view.Items = append(view.Items, item)
	}
	return view, nil
}

// SetQuantity sets the quantity of one orderable SKU.
func (s *Service) SetQuantity(ctx context.Context, userID, skuID string, quantity int64) error {
	if quantity < MinQuantity || quantity > MaxQuantity {
		return ErrInvalidQuantity
	}
	if _, err := s.pricer.GetSkuPrice(ctx, skuID); err != nil {
		return err
	}
	current, err := s.store.Get(ctx, userID)
	if err != nil {
		return err
	}
	if _, exists := current[skuID]; !exists && len(current) >= MaxLines {
		return ErrCartFull
	}
	return s.store.SetMany(ctx, userID, map[string]int64{skuID: quantity})
}

// Remove deletes one line; removing a line that is not there is not an error.
func (s *Service) Remove(ctx context.Context, userID, skuID string) error {
	return s.store.Remove(ctx, userID, skuID)
}

// Merge adds guest lines to the cart, keeping the larger quantity per SKU (capped at 99) so a retried merge
// changes nothing. Invalid, unknown or closed SKUs are skipped, not failed.
func (s *Service) Merge(ctx context.Context, userID string, guest []Line) ([]Skipped, error) {
	current, err := s.store.Get(ctx, userID)
	if err != nil {
		return nil, err
	}
	wanted := map[string]int64{}
	var skipped []Skipped
	for _, line := range guest {
		if line.Quantity < MinQuantity {
			skipped = append(skipped, Skipped{SkuID: line.SkuID, Reason: "INVALID_QUANTITY"})
			continue
		}
		wanted[line.SkuID] = max(wanted[line.SkuID], min(line.Quantity, MaxQuantity))
	}
	ids := make([]string, 0, len(wanted))
	for id := range wanted {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	writes := map[string]int64{}
	size := len(current)
	for _, id := range ids {
		if _, err := s.pricer.GetSkuPrice(ctx, id); err != nil {
			reason, ok := skipReason(err)
			if !ok {
				return nil, err
			}
			skipped = append(skipped, Skipped{SkuID: id, Reason: reason})
			continue
		}
		have, exists := current[id]
		if !exists {
			if size >= MaxLines {
				skipped = append(skipped, Skipped{SkuID: id, Reason: "CART_FULL"})
				continue
			}
			size++
		}
		if wanted[id] > have {
			writes[id] = wanted[id]
		}
	}
	if len(writes) > 0 {
		if err := s.store.SetMany(ctx, userID, writes); err != nil {
			return nil, err
		}
	}
	return skipped, nil
}

func skipReason(err error) (string, bool) {
	switch {
	case errors.Is(err, repository.ErrSkuNotFound):
		return "SKU_NOT_FOUND", true
	case errors.Is(err, repository.ErrSkuNotOrderable):
		return "SKU_NOT_ORDERABLE", true
	}
	return "", false
}
