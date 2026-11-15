package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"regexp"
	"strconv"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/internalauth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/request"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/cart"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/repository"
)

var skuIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

type cartAPI struct {
	svc *cart.Service
}

// registerCartRoutes serves the signed-in customer's cart. The cart is always the token subject's.
func registerCartRoutes(mux *http.ServeMux, svc *cart.Service) {
	api := &cartAPI{svc: svc}
	mine := func(h http.HandlerFunc) http.Handler { return auth.RequireAuth(h) }
	mux.Handle("GET /api/v1/carts/me", mine(api.get))
	mux.Handle("PUT /api/v1/carts/me/items/{skuId}", mine(api.put))
	mux.Handle("DELETE /api/v1/carts/me/items/{skuId}", mine(api.remove))
	mux.Handle("POST /api/v1/carts/me/merge", mine(api.merge))
	// The order service empties what was bought once the order is confirmed; customers cannot call it.
	mux.Handle("POST /api/v1/carts/internal/ordered-items", internalauth.Require(http.HandlerFunc(api.removeOrdered)))
}

// removeOrdered removes the purchased SKUs from a customer's cart. Only those lines go: anything the customer
// added after the quote stays. Removing a line that is not there is fine, so the call is idempotent.
func (a *cartAPI) removeOrdered(w http.ResponseWriter, r *http.Request) {
	var body struct {
		UserID string   `json:"userId"`
		SkuIDs []string `json:"skuIds"`
	}
	if !request.DecodeJSON(w, r, &body) {
		return
	}
	if body.UserID == "" || len(body.UserID) > 64 || len(body.SkuIDs) == 0 || len(body.SkuIDs) > cart.MaxLines {
		response.Error(w, r, http.StatusBadRequest, "VALIDATION_FAILED", "The request has invalid fields",
			map[string][]string{"userId": {"userId and 1 to 50 skuIds are required"}})
		return
	}
	for _, id := range body.SkuIDs {
		if !skuIDPattern.MatchString(id) {
			response.Error(w, r, http.StatusBadRequest, "VALIDATION_FAILED", "The request has invalid fields",
				map[string][]string{"skuIds": {"every skuId must be valid"}})
			return
		}
	}
	for _, id := range body.SkuIDs {
		if err := a.svc.Remove(r.Context(), body.UserID, id); err != nil {
			writeCartError(w, r, err)
			return
		}
	}
	response.JSON(w, http.StatusOK, "Ordered items removed from the cart", map[string]any{"removed": len(body.SkuIDs)}, nil)
}

func userIDOf(r *http.Request) string {
	claims, _ := auth.ClaimsFrom(r.Context())
	return claims.UserID
}

func (a *cartAPI) get(w http.ResponseWriter, r *http.Request) {
	a.writeCart(w, r, nil)
}

func (a *cartAPI) put(w http.ResponseWriter, r *http.Request) {
	skuID := r.PathValue("skuId")
	if !validSkuID(w, r, skuID) {
		return
	}
	var body struct {
		// Read as a number literal so a fractional quantity is rejected with a clear message.
		Quantity json.Number `json:"quantity"`
	}
	if !request.DecodeJSON(w, r, &body) {
		return
	}
	quantity, err := strconv.ParseInt(body.Quantity.String(), 10, 64)
	if err != nil {
		writeCartError(w, r, cart.ErrInvalidQuantity)
		return
	}
	if err := a.svc.SetQuantity(r.Context(), userIDOf(r), skuID, quantity); err != nil {
		writeCartError(w, r, err)
		return
	}
	a.writeCart(w, r, nil)
}

func (a *cartAPI) remove(w http.ResponseWriter, r *http.Request) {
	skuID := r.PathValue("skuId")
	if !validSkuID(w, r, skuID) {
		return
	}
	if err := a.svc.Remove(r.Context(), userIDOf(r), skuID); err != nil {
		writeCartError(w, r, err)
		return
	}
	a.writeCart(w, r, nil)
}

func (a *cartAPI) merge(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Items []cart.Line `json:"items"`
	}
	if !request.DecodeJSON(w, r, &body) {
		return
	}
	switch {
	case len(body.Items) == 0:
		response.Error(w, r, http.StatusBadRequest, "VALIDATION_FAILED", "The request has invalid fields",
			map[string][]string{"items": {"items must list at least one line"}})
		return
	case len(body.Items) > cart.MaxLines:
		response.Error(w, r, http.StatusBadRequest, "VALIDATION_FAILED", "The request has invalid fields",
			map[string][]string{"items": {"items must list at most 50 lines"}})
		return
	}
	for _, line := range body.Items {
		if !skuIDPattern.MatchString(line.SkuID) {
			response.Error(w, r, http.StatusBadRequest, "VALIDATION_FAILED", "The request has invalid fields",
				map[string][]string{"items": {"every line needs a valid skuId"}})
			return
		}
	}
	skipped, err := a.svc.Merge(r.Context(), userIDOf(r), body.Items)
	if err != nil {
		writeCartError(w, r, err)
		return
	}
	if skipped == nil {
		skipped = []cart.Skipped{}
	}
	a.writeCart(w, r, skipped)
}

type cartBody struct {
	cart.View
	Skipped []cart.Skipped `json:"skipped,omitempty"`
}

func (a *cartAPI) writeCart(w http.ResponseWriter, r *http.Request, skipped []cart.Skipped) {
	view, err := a.svc.View(r.Context(), userIDOf(r))
	if err != nil {
		writeCartError(w, r, err)
		return
	}
	response.JSON(w, http.StatusOK, "Cart", cartBody{View: view, Skipped: skipped}, nil)
}

func validSkuID(w http.ResponseWriter, r *http.Request, skuID string) bool {
	if skuIDPattern.MatchString(skuID) {
		return true
	}
	response.Error(w, r, http.StatusBadRequest, "VALIDATION_FAILED", "The request has invalid fields",
		map[string][]string{"skuId": {"skuId is not valid"}})
	return false
}

func writeCartError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, cart.ErrInvalidQuantity):
		response.Error(w, r, http.StatusBadRequest, "VALIDATION_FAILED", "The request has invalid fields",
			map[string][]string{"quantity": {"quantity must be a whole number from 1 to 99"}})
	case errors.Is(err, cart.ErrCartFull):
		response.Error(w, r, http.StatusConflict, "CART_FULL", err.Error(), nil)
	case errors.Is(err, repository.ErrSkuNotFound):
		response.Error(w, r, http.StatusNotFound, "SKU_NOT_FOUND", "SKU not found", nil)
	case errors.Is(err, repository.ErrSkuNotOrderable):
		response.Error(w, r, http.StatusConflict, "SKU_NOT_ORDERABLE", "SKU cannot be ordered right now", nil)
	case dependency.IsUnavailable(err):
		dependency.WriteUnavailable(w, r, err)
	default:
		log.Printf("[catalog] cart request failed: %v", err)
		response.Error(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal server error", nil)
	}
}
