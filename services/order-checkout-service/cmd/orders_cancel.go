package main

import (
	"errors"
	"log"
	"net/http"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/checkout"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/orders"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/saga"
)

// cancelView is what the customer gets back from a cancel request.
type cancelView struct {
	OrderID          string      `json:"orderId"`
	Status           string      `json:"status"`
	PaymentStatus    string      `json:"paymentStatus"`
	RefundObligation *refundView `json:"refundObligation"`
}

// cancelOrder is the customer's cancel request (B05). The owner is the token subject, never a parameter; another
// customer's order answers 404. Repeating the request answers the stored result and changes nothing.
func cancelOrder(svc *checkout.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, _ := auth.ClaimsFrom(r.Context())
		o, err := svc.Cancel(r.Context(), claims.UserID, r.PathValue("orderId"))
		if err != nil {
			writeCancelError(w, r, err)
			return
		}
		view := cancelView{OrderID: o.OrderID, Status: o.Status, PaymentStatus: o.PaymentStatus}
		if o.RefundPendingVND > 0 {
			view.RefundObligation = &refundView{AmountVND: o.RefundPendingVND, Status: "PENDING"}
		}
		if o.Status == string(saga.StateCancelRequested) {
			// The warehouse is picking or packing: the cancellation is not promised until it confirms the stop (SC06).
			response.JSON(w, http.StatusAccepted, "Cancellation requested. Waiting for the warehouse to confirm it stopped the parcel.", view, nil)
			return
		}
		response.JSON(w, http.StatusOK, "Order cancelled", view, nil)
	}
}

func writeCancelError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, orders.ErrNotFound):
		response.Error(w, r, http.StatusNotFound, "ORDER_NOT_FOUND", "Order not found", nil)
	case errors.Is(err, checkout.ErrHandedOver):
		response.Error(w, r, http.StatusConflict, "ORDER_HANDED_OVER", "The parcel was already handed to the carrier; refuse it at delivery or return it after delivery", nil)
	case errors.Is(err, checkout.ErrPaymentUnresolved):
		response.Error(w, r, http.StatusConflict, "PAYMENT_UNRESOLVED", "The payment result is not known yet; try again once it is resolved", nil)
	case errors.Is(err, checkout.ErrNotCancellable):
		response.Error(w, r, http.StatusConflict, "ORDER_NOT_CANCELLABLE", "This order cannot be cancelled", nil)
	case errors.Is(err, orders.ErrStaleState):
		response.Error(w, r, http.StatusConflict, "ORDER_STATE_CHANGED", "The order changed while it was being cancelled; retry", nil)
	case dependency.IsUnavailable(err):
		dependency.WriteUnavailable(w, r, err)
	default:
		log.Printf("[order] cancel failed: %v", err)
		response.Error(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal server error", nil)
	}
}
