package checkout

import (
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/orders"
)

// ActiveWarehouses defines the Stage 3 allocation priority order.
var ActiveWarehouses = []string{"WH-HCM-01", "WH-HN-01"}

// PlanParcels selects 1 warehouse when one warehouse can satisfy all items, or splits across at most
// 2 warehouses (WH-HCM-01, WH-HN-01) when combined stock satisfies the order (no extra shipping fee).
func PlanParcels(items []orders.Item, totalPayableVND int64, isCOD bool, breakdown map[string]map[string]int64) ([]orders.Parcel, bool) {
	if len(items) == 0 {
		return nil, false
	}

	// 1. Prefer single warehouse when possible (zero unnecessary splitting).
	for _, wh := range ActiveWarehouses {
		whStock := breakdown[wh]
		fitsAll := true
		for _, it := range items {
			if whStock[it.SkuID] < it.Quantity {
				fitsAll = false
				break
			}
		}
		if fitsAll {
			cod := int64(0)
			if isCOD {
				cod = totalPayableVND
			}
			cloned := append([]orders.Item(nil), items...)
			return []orders.Parcel{{
				ParcelSeq:    1,
				WarehouseID:  wh,
				Items:        cloned,
				CodAmountVND: cod,
				Status:       orders.StatusPending,
			}}, true
		}
	}

	// 2. Split across WH-HCM-01 and WH-HN-01 (max 2 parcels).
	var parcels []orders.Parcel
	remaining := make(map[string]int64, len(items))
	bySku := make(map[string]orders.Item, len(items))
	for _, it := range items {
		remaining[it.SkuID] = it.Quantity
		bySku[it.SkuID] = it
	}

	for _, wh := range ActiveWarehouses {
		whStock := breakdown[wh]
		var whItems []orders.Item
		for _, orig := range items {
			need := remaining[orig.SkuID]
			if need <= 0 {
				continue
			}
			avail := whStock[orig.SkuID]
			if avail <= 0 {
				continue
			}
			take := min(need, avail)
			remaining[orig.SkuID] = need - take
			part := bySku[orig.SkuID]
			part.Quantity = take
			whItems = append(whItems, part)
		}
		if len(whItems) > 0 {
			parcels = append(parcels, orders.Parcel{
				ParcelSeq:   len(parcels) + 1,
				WarehouseID: wh,
				Items:       whItems,
				Status:      orders.StatusPending,
			})
		}
	}

	for _, rem := range remaining {
		if rem > 0 {
			return nil, false
		}
	}

	// 3. Allocate COD collection amount across parcels so sum(codAmount) == totalPayableVND.
	if isCOD && len(parcels) > 0 {
		var totalMerch int64
		parcelMerch := make([]int64, len(parcels))
		for i, p := range parcels {
			var sub int64
			for _, it := range p.Items {
				sub += it.Quantity * it.AllocatedUnitPaidVND
			}
			parcelMerch[i] = sub
			totalMerch += sub
		}
		var assigned int64
		for i := 1; i < len(parcels); i++ {
			var amt int64
			if totalMerch > 0 {
				amt = (totalPayableVND * parcelMerch[i]) / totalMerch
			}
			parcels[i].CodAmountVND = amt
			assigned += amt
		}
		parcels[0].CodAmountVND = totalPayableVND - assigned
	}

	return parcels, true
}
