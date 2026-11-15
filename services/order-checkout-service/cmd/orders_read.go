package main

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/contracts/ids"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/checkout"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/orders"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/saga"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/snapshot"
)

const (
	defaultPageSize = 20
	maxPageSize     = 100
)

func registerOrderReadRoutes(mux *http.ServeMux, svc *checkout.Service) {
	customer := func(h http.HandlerFunc) http.Handler { return auth.RequireAuth(auth.RequireRole("CUSTOMER")(h)) }
	mux.Handle("GET /api/v1/orders", customer(listMyOrders(svc)))
	mux.Handle("GET /api/v1/orders/{orderId}", customer(getMyOrder(svc)))
	registerOrderAdminRoutes(mux, svc)
}

// orderSummary is one row of the customer's order history.
type orderSummary struct {
	OrderID         string     `json:"orderId"`
	Status          string     `json:"status"`
	PaymentStatus   string     `json:"paymentStatus"`
	FailureCode     string     `json:"failureCode,omitempty"`
	VoucherCode     string     `json:"voucherCode,omitempty"`
	TotalPayableVND int64      `json:"totalPayableVnd"`
	ItemCount       int64      `json:"itemCount"`
	ExpiresAt       *time.Time `json:"expiresAt,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

func summaryOf(o orders.Order) orderSummary {
	return orderSummary{OrderID: o.OrderID, Status: o.Status, PaymentStatus: o.PaymentStatus, FailureCode: o.FailureCode,
		VoucherCode: o.VoucherCode, TotalPayableVND: o.TotalPayableVND, ItemCount: o.ItemCount, ExpiresAt: o.ExpiresAt,
		CreatedAt: o.CreatedAt, UpdatedAt: o.UpdatedAt}
}

// pageParams reads page (from 1) and limit (1 to 100, default 20). It writes the 400 itself when they are invalid.
func pageParams(w http.ResponseWriter, r *http.Request) (page, limit int, ok bool) {
	page, limit = 1, defaultPageSize
	q := r.URL.Query()
	var err error
	if v := q.Get("page"); v != "" {
		if page, err = strconv.Atoi(v); err != nil || page < 1 {
			badParam(w, r, "page", "page must be a positive integer")
			return 0, 0, false
		}
	}
	if v := q.Get("limit"); v != "" {
		if limit, err = strconv.Atoi(v); err != nil || limit < 1 || limit > maxPageSize {
			badParam(w, r, "limit", "limit must be between 1 and 100")
			return 0, 0, false
		}
	}
	return page, limit, true
}

func badParam(w http.ResponseWriter, r *http.Request, name, msg string) {
	response.Error(w, r, http.StatusBadRequest, "VALIDATION_FAILED", "The request has invalid parameters", map[string][]string{name: {msg}})
}

// statusParam reads the optional ?status= filter; it must be a known order status.
func statusParam(w http.ResponseWriter, r *http.Request) ([]string, bool) {
	v := r.URL.Query().Get("status")
	if v == "" {
		return nil, true
	}
	switch saga.State(v) {
	case saga.StatePending, saga.StateReserved, saga.StateUnknownHold, saga.StateConfirmed, saga.StateInFulfillment, saga.StateDelivered, saga.StateFailedClosed, saga.StateCancelRequested, saga.StateCancelled:
		return []string{v}, true
	}
	badParam(w, r, "status", "status must be PENDING, RESERVED, UNKNOWN_HOLD, CONFIRMED, IN_FULFILLMENT, DELIVERED, FAILED_CLOSED, CANCEL_REQUESTED or CANCELLED")
	return nil, false
}

func pageMeta(total, page, limit int) *response.Metadata {
	return &response.Metadata{Total: int64(total), Page: page, PageCount: (total + limit - 1) / limit, Limit: limit}
}

// listMyOrders: the caller's order history, newest first. The owner is the token subject, never a parameter.
func listMyOrders(svc *checkout.Service) http.HandlerFunc {
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
		list, total, err := svc.Orders.Query(r.Context(), orders.Filter{UserID: claims.UserID, Statuses: statuses, Offset: (page - 1) * limit, Limit: limit})
		if err != nil {
			writeReadError(w, r, err)
			return
		}
		out := make([]orderSummary, len(list))
		for i, o := range list {
			out[i] = summaryOf(o)
		}
		response.JSON(w, http.StatusOK, "Orders", out, pageMeta(total, page, limit))
	}
}

type paymentView struct {
	AttemptID         string `json:"attemptId,omitempty"`
	Status            string `json:"status"`
	ExpectedAmountVND int64  `json:"expectedAmountVnd"`
	CapturedAmountVND int64  `json:"capturedAmountVnd"`
}

// refundView is the money owed back for the order: AmountVND is the whole refund, RefundedAmountVND what was
// already paid out, and Status is PENDING until all of it was paid (DONE after).
type refundView struct {
	AmountVND         int64  `json:"amountVnd"`
	RefundedAmountVND int64  `json:"refundedAmountVnd"`
	Status            string `json:"status"`
}

// refundOf describes the refund of an order, nil when nothing was ever owed.
func refundOf(o orders.Order) *refundView {
	total := o.RefundPendingVND + o.RefundedVND
	switch {
	case o.RefundPendingVND > 0:
		return &refundView{AmountVND: total, RefundedAmountVND: o.RefundedVND, Status: "PENDING"}
	case o.RefundedVND > 0:
		return &refundView{AmountVND: total, RefundedAmountVND: o.RefundedVND, Status: "DONE"}
	}
	return nil
}

// orderDetail is the customer's view of one order: the frozen purchase-time snapshot, the payment and any refund
// owed. Internal fields (warehouse, owner id) are not part of it.
type orderDetail struct {
	orderSummary
	QuoteID          string            `json:"quoteId"`
	SubtotalVND      int64             `json:"subtotalVnd"`
	DiscountVND      int64             `json:"discountVnd"`
	ShippingFeeVND   int64             `json:"shippingFeeVnd"`
	Items            []snapshot.Line   `json:"items"`
	ShippingAddress  *snapshot.Address `json:"shippingAddress"`
	Payment          paymentView       `json:"payment"`
	RefundObligation *refundView       `json:"refundObligation"`
	Version          int64             `json:"version"`
}

func getMyOrder(svc *checkout.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, _ := auth.ClaimsFrom(r.Context())
		o, err := svc.Orders.Get(r.Context(), r.PathValue("orderId"))
		if errors.Is(err, orders.ErrNotFound) || (err == nil && o.UserID != claims.UserID) {
			response.Error(w, r, http.StatusNotFound, "ORDER_NOT_FOUND", "Order not found", nil)
			return
		}
		if err != nil {
			writeReadError(w, r, err)
			return
		}
		response.JSON(w, http.StatusOK, "Order", detailOf(r, svc, o), nil)
	}
}

// detailOf builds the detail. The snapshot is the source of the lines and the address; when the snapshot store
// cannot answer, the lines of the order row (same frozen values) are used and the address is left out.
func detailOf(r *http.Request, svc *checkout.Service, o orders.Order) orderDetail {
	d := orderDetail{
		orderSummary: summaryOf(o), QuoteID: o.QuoteID, SubtotalVND: o.SubtotalVND, DiscountVND: o.DiscountVND,
		ShippingFeeVND: o.ShippingFeeVND, Version: o.Version,
		Payment: paymentView{Status: o.PaymentStatus, ExpectedAmountVND: o.TotalPayableVND, CapturedAmountVND: o.CapturedVND},
	}
	d.ItemCount = 0
	for _, it := range o.Items {
		d.ItemCount += it.Quantity
	}
	if o.Status != string(saga.StateFailedClosed) || o.CapturedVND > 0 || o.PaymentStatus != saga.PayUnpaid {
		d.Payment.AttemptID = ids.PaymentAttemptID(o.OrderID)
	}
	d.RefundObligation = refundOf(o)
	snap, found, err := svc.Snapshots.Get(r.Context(), o.OrderID)
	if err != nil {
		log.Printf("[order] snapshot of order %s unavailable: %s", o.OrderID, "dependency unavailable")
	}
	if err == nil && found {
		d.Items, d.ShippingAddress = snap.Lines, &snap.ShippingAddress
		return d
	}
	d.Items = make([]snapshot.Line, len(o.Items))
	for i, it := range o.Items {
		d.Items[i] = snapshot.Line{SkuID: it.SkuID, SkuName: it.SkuName, Quantity: it.Quantity, UnitPriceVND: it.UnitPriceVND,
			LineDiscountVND: it.LineDiscountVND, AllocatedUnitPaidVND: it.AllocatedUnitPaidVND}
	}
	return d
}

func writeReadError(w http.ResponseWriter, r *http.Request, err error) {
	if dependency.IsUnavailable(err) {
		dependency.WriteUnavailable(w, r, err)
		return
	}
	log.Printf("[order] read failed: %v", err)
	response.Error(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal server error", nil)
}
