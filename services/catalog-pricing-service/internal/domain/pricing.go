package domain

import (
	"errors"
	"sort"
	"time"
)

const (
	StandardShippingFeeVND int64 = 30000
	FreeShippingThreshold  int64 = 1000000
	QuoteTTL                     = 5 * time.Minute // P02: Báo giá hiệu lực 5 phút
)

type QuoteLineInput struct {
	SkuID        string `json:"skuId"`
	SkuName      string `json:"skuName"`
	Quantity     int64  `json:"quantity"`
	UnitPriceVND int64  `json:"unitPriceVnd"`
}

type QuoteLineAllocation struct {
	SkuID                string `json:"skuId"`
	SkuName              string `json:"skuName"`
	Quantity             int64  `json:"quantity"`
	UnitPriceVND         int64  `json:"unitPriceVnd"`
	LineSubtotalVND      int64  `json:"lineSubtotalVnd"`
	LineDiscountVND      int64  `json:"lineDiscountVnd"`
	LineNetPayableVND    int64  `json:"lineNetPayableVnd"`
	AllocatedUnitPaidVND int64  `json:"allocatedUnitPaidVnd"` // SC07: Giá thực trả mỗi chiếc (VD: 270.000đ)
}

type Quote struct {
	QuoteID         string                `json:"quoteId"`
	UserID          string                `json:"userId"`
	VoucherCode     string                `json:"voucherCode,omitempty"`
	DiscountPct     int64                 `json:"discountPct"`
	SubtotalVND     int64                 `json:"subtotalVnd"`
	DiscountVND     int64                 `json:"discountVnd"`
	ShippingFeeVND  int64                 `json:"shippingFeeVnd"`
	TotalPayableVND int64                 `json:"totalPayableVnd"`
	Lines           []QuoteLineAllocation `json:"lines"`
	ExpiresAt       time.Time             `json:"expiresAt"`
}

// CalculateQuote thực thi P02, P08, SC01, SC07 và quy tắc làm tròn nguyên đồng SC10
func CalculateQuote(quoteID, userID, voucherCode string, discountPct int64, inputs []QuoteLineInput, now time.Time) (*Quote, error) {
	if len(inputs) == 0 {
		return nil, errors.New("giỏ hàng trống")
	}

	sorted := make([]QuoteLineInput, len(inputs))
	copy(sorted, inputs)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].SkuID < sorted[j].SkuID
	})

	var subtotal int64
	for _, in := range sorted {
		if in.Quantity <= 0 || in.UnitPriceVND < 0 {
			return nil, errors.New("số lượng hoặc đơn giá không hợp lệ")
		}
		subtotal += in.Quantity * in.UnitPriceVND
	}

	totalDiscount := (subtotal * discountPct) / 100
	var allocatedSum int64
	lines := make([]QuoteLineAllocation, len(sorted))

	type remainderItem struct {
		idx       int
		remainder int64
	}
	remainders := make([]remainderItem, len(sorted))

	for i, in := range sorted {
		lineSub := in.Quantity * in.UnitPriceVND
		var exactDisc int64
		var rem int64
		if subtotal > 0 && totalDiscount > 0 {
			exactDisc = (totalDiscount * lineSub) / subtotal
			rem = (totalDiscount * lineSub) % subtotal
		}
		allocatedSum += exactDisc
		remainders[i] = remainderItem{idx: i, remainder: rem}
		lines[i] = QuoteLineAllocation{
			SkuID:           in.SkuID,
			SkuName:         in.SkuName,
			Quantity:        in.Quantity,
			UnitPriceVND:    in.UnitPriceVND,
			LineSubtotalVND: lineSub,
			LineDiscountVND: exactDisc,
		}
	}

	// SC10: Phân bổ phần dư theo phần lẻ lớn nhất, hòa thì ưu tiên thứ tự SKU ổn định
	leftover := totalDiscount - allocatedSum
	if leftover > 0 {
		sort.SliceStable(remainders, func(i, j int) bool {
			if remainders[i].remainder == remainders[j].remainder {
				return remainders[i].idx < remainders[j].idx
			}
			return remainders[i].remainder > remainders[j].remainder
		})
		for k := int64(0); k < leftover && int(k) < len(remainders); k++ {
			targetIdx := remainders[k].idx
			lines[targetIdx].LineDiscountVND++
		}
	}

	for i := range lines {
		lines[i].LineNetPayableVND = lines[i].LineSubtotalVND - lines[i].LineDiscountVND
		lines[i].AllocatedUnitPaidVND = lines[i].LineNetPayableVND / lines[i].Quantity
	}

	netMerchandise := subtotal - totalDiscount
	shippingFee := StandardShippingFeeVND
	if netMerchandise >= FreeShippingThreshold {
		shippingFee = 0
	}

	return &Quote{
		QuoteID:         quoteID,
		UserID:          userID,
		VoucherCode:     voucherCode,
		DiscountPct:     discountPct,
		SubtotalVND:     subtotal,
		DiscountVND:     totalDiscount,
		ShippingFeeVND:  shippingFee,
		TotalPayableVND: netMerchandise + shippingFee,
		Lines:           lines,
		ExpiresAt:       now.UTC().Add(QuoteTTL),
	}, nil
}
