package main

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/checkout"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/orders"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/snapshot"
)

// Staff roles that may read orders. Support and operations see every order; a warehouse manager only the orders
// of the warehouses in their token scope.
const (
	roleSupportAgent = "SUPPORT_AGENT"
	roleSupportLead  = "SUPPORT_LEAD"
	roleOpsMgr       = "OPS_MGR"
	roleWarehouseMgr = "WAREHOUSE_MGR"
)

func registerOrderAdminRoutes(mux *http.ServeMux, svc *checkout.Service) {
	staff := func(h http.HandlerFunc) http.Handler {
		return auth.RequireAuth(auth.RequireRole(roleSupportAgent, roleSupportLead, roleOpsMgr, roleWarehouseMgr)(h))
	}
	mux.Handle("GET /api/v1/admin/orders", staff(listStaffOrders(svc)))
	mux.Handle("GET /api/v1/admin/orders/{orderId}", staff(getStaffOrder(svc)))
}

// maskName keeps the first letter of every word ("Nguyen Van Lan" -> "N*** V*** L***").
func maskName(name string) string {
	words := strings.Fields(name)
	for i, w := range words {
		words[i] = string([]rune(w)[:1]) + "***"
	}
	return strings.Join(words, " ")
}

// maskPhone keeps the last three digits ("0901234567" -> "*******567").
func maskPhone(phone string) string {
	r := []rune(strings.TrimSpace(phone))
	keep := 3
	if len(r) <= keep {
		return strings.Repeat("*", len(r))
	}
	return strings.Repeat("*", len(r)-keep) + string(r[len(r)-keep:])
}

// staffContact is all staff learn about the recipient: masked name and phone, district and province. The street,
// ward and the real name and phone are never part of a staff response.
type staffContact struct {
	Recipient string `json:"recipient,omitempty"`
	Phone     string `json:"phone,omitempty"`
	District  string `json:"district,omitempty"`
	Province  string `json:"province,omitempty"`
}

func contactOf(a snapshot.Address) staffContact {
	return staffContact{Recipient: maskName(a.Recipient), Phone: maskPhone(a.Phone), District: a.District, Province: a.Province}
}

type staffOrderRow struct {
	OrderID         string    `json:"orderId"`
	CustomerID      string    `json:"customerId"`
	Status          string    `json:"status"`
	PaymentMethod   string    `json:"paymentMethod,omitempty"`
	PaymentStatus   string    `json:"paymentStatus"`
	FailureCode     string    `json:"failureCode,omitempty"`
	WarehouseID     string    `json:"warehouseId"`
	TotalPayableVND int64     `json:"totalPayableVnd"`
	ItemCount       int64     `json:"itemCount"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
	staffContact
}

// warehouseScope is the warehouses a caller may read orders of: nil means all. A warehouse manager without any
// other staff role is limited to their token scopes (an empty scope list sees nothing).
func warehouseScope(c *auth.Claims) (ids []string, limited bool) {
	if !c.HasRole(roleWarehouseMgr) || c.HasRole(roleSupportAgent) || c.HasRole(roleSupportLead) || c.HasRole(roleOpsMgr) {
		return nil, false
	}
	for _, s := range c.WarehouseScopes {
		if s == auth.AllWarehouses {
			return nil, false
		}
	}
	return c.WarehouseScopes, true
}

func listStaffOrders(svc *checkout.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		page, limit, ok := pageParams(w, r)
		if !ok {
			return
		}
		statuses, ok := statusParam(w, r)
		if !ok {
			return
		}
		claims, _ := auth.ClaimsFrom(r.Context())
		scope, limited := warehouseScope(claims)
		if wh := strings.TrimSpace(r.URL.Query().Get("warehouse")); wh != "" {
			if !auth.CanAccessWarehouse(claims, wh) && limited {
				auth.ForbiddenWarehouse(w, r)
				return
			}
			scope, limited = []string{wh}, true
		}
		if limited && len(scope) == 0 {
			response.JSON(w, http.StatusOK, "Orders", []staffOrderRow{}, pageMeta(0, page, limit))
			return
		}
		list, total, err := svc.Orders.Query(r.Context(), orders.Filter{Statuses: statuses, WarehouseIDs: scope, Offset: (page - 1) * limit, Limit: limit})
		if err != nil {
			writeReadError(w, r, err)
			return
		}
		rows := make([]staffOrderRow, len(list))
		for i, o := range list {
			rows[i] = staffOrderRow{OrderID: o.OrderID, CustomerID: o.UserID, Status: o.Status, PaymentMethod: o.PaymentMethod, PaymentStatus: o.PaymentStatus, FailureCode: o.FailureCode,
				WarehouseID: o.WarehouseID, TotalPayableVND: o.TotalPayableVND, ItemCount: o.ItemCount, CreatedAt: o.CreatedAt, UpdatedAt: o.UpdatedAt}
			if snap, found, err := svc.Snapshots.Get(r.Context(), o.OrderID); err == nil && found {
				rows[i].staffContact = contactOf(snap.ShippingAddress)
			}
		}
		response.JSON(w, http.StatusOK, "Orders", rows, pageMeta(total, page, limit))
	}
}

type staffOrderDetail struct {
	staffOrderRow
	QuoteID          string          `json:"quoteId"`
	VoucherCode      string          `json:"voucherCode,omitempty"`
	SubtotalVND      int64           `json:"subtotalVnd"`
	DiscountVND      int64           `json:"discountVnd"`
	ShippingFeeVND   int64           `json:"shippingFeeVnd"`
	Items            []snapshot.Line `json:"items"`
	Payment          paymentView     `json:"payment"`
	RefundObligation *refundView     `json:"refundObligation"`
}

func getStaffOrder(svc *checkout.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		o, err := svc.Orders.Get(r.Context(), r.PathValue("orderId"))
		if errors.Is(err, orders.ErrNotFound) {
			response.Error(w, r, http.StatusNotFound, "ORDER_NOT_FOUND", "Order not found", nil)
			return
		}
		if err != nil {
			writeReadError(w, r, err)
			return
		}
		claims, _ := auth.ClaimsFrom(r.Context())
		if _, limited := warehouseScope(claims); limited && !auth.CanAccessWarehouse(claims, o.WarehouseID) {
			auth.ForbiddenWarehouse(w, r)
			return
		}
		own := detailOf(r, svc, o)
		d := staffOrderDetail{
			staffOrderRow: staffOrderRow{OrderID: o.OrderID, CustomerID: o.UserID, Status: o.Status, PaymentMethod: o.PaymentMethod, PaymentStatus: o.PaymentStatus, FailureCode: o.FailureCode,
				WarehouseID: o.WarehouseID, TotalPayableVND: o.TotalPayableVND, ItemCount: own.ItemCount, CreatedAt: o.CreatedAt, UpdatedAt: o.UpdatedAt},
			QuoteID: o.QuoteID, VoucherCode: o.VoucherCode, SubtotalVND: o.SubtotalVND, DiscountVND: o.DiscountVND, ShippingFeeVND: o.ShippingFeeVND,
			Items: own.Items, Payment: own.Payment, RefundObligation: own.RefundObligation,
		}
		if own.ShippingAddress != nil {
			d.staffContact = contactOf(*own.ShippingAddress)
		}
		response.JSON(w, http.StatusOK, "Order", d, nil)
	}
}
