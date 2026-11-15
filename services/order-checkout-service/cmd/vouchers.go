package main

import (
	"log"
	"net/http"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/voucher"
)

func registerVoucherRoutes(mux *http.ServeMux, svc *voucher.Service) {
	mux.Handle("GET /api/v1/vouchers/{code}/eligibility", auth.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := auth.ClaimsFrom(r.Context())
		result, err := svc.Check(r.Context(), r.PathValue("code"), claims.UserID)
		if err != nil {
			writeVoucherError(w, r, err)
			return
		}
		response.JSON(w, http.StatusOK, "Voucher eligibility", result, nil)
	})))
}

func writeVoucherError(w http.ResponseWriter, r *http.Request, err error) {
	if dependency.IsUnavailable(err) {
		dependency.WriteUnavailable(w, r, err)
		return
	}
	log.Printf("[order] voucher request failed: %v", err)
	response.Error(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal server error", nil)
}
