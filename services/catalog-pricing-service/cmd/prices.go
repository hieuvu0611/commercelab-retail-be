package main

import (
	"encoding/json"
	"errors"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/request"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/domain"
)

// RoleMarketing may set prices.
const RoleMarketing = "MARKETING"

type priceAPI struct {
	svc *domain.PriceService
}

func registerPriceRoutes(mux *http.ServeMux, svc *domain.PriceService) {
	api := &priceAPI{svc: svc}
	marketing := func(h http.HandlerFunc) http.Handler {
		return auth.RequireAuth(auth.RequireRole(RoleMarketing)(h))
	}
	mux.Handle("GET /api/v1/admin/prices", marketing(api.list))
	mux.Handle("PUT /api/v1/admin/skus/{skuId}/price", marketing(api.set))
	mux.Handle("GET /api/v1/admin/skus/{skuId}/price", marketing(api.get))
}

func (a *priceAPI) set(w http.ResponseWriter, r *http.Request) {
	var body struct {
		// UnitPriceVND is read as a number literal so a fractional amount can be rejected with a clear message.
		UnitPriceVND  json.Number `json:"unitPriceVnd"`
		EffectiveFrom *time.Time  `json:"effectiveFrom"`
	}
	if !request.DecodeJSON(w, r, &body) {
		return
	}
	amount, err := strconv.ParseInt(body.UnitPriceVND.String(), 10, 64)
	if err != nil {
		response.Error(w, r, http.StatusBadRequest, "VALIDATION_FAILED", "The request has invalid fields",
			map[string][]string{"unitPriceVnd": {"unitPriceVnd is required and must be a whole number of VND"}})
		return
	}
	view, err := a.svc.SetPrice(r.Context(), actorOf(r), r.PathValue("skuId"), amount, body.EffectiveFrom)
	if err != nil {
		writePriceError(w, r, err)
		return
	}
	response.JSON(w, http.StatusOK, "Price saved", view, nil)
}

func (a *priceAPI) list(w http.ResponseWriter, r *http.Request) {
	page, okPage := queryInt(r, "page", 1)
	limit, okLimit := queryInt(r, "limit", 20)
	if !okPage || !okLimit {
		response.Error(w, r, http.StatusBadRequest, "VALIDATION_FAILED", "page and limit must be positive integers", nil)
		return
	}
	limit = min(limit, 100)
	rows, total, err := a.svc.ListPrices(r.Context(), strings.TrimSpace(r.URL.Query().Get("q")), page, limit)
	if err != nil {
		writePriceError(w, r, err)
		return
	}
	response.JSON(w, http.StatusOK, "Prices", rows, &response.Metadata{
		Total: total, Page: page, Limit: limit, PageCount: int(math.Ceil(float64(total) / float64(limit))),
	})
}

func (a *priceAPI) get(w http.ResponseWriter, r *http.Request) {
	view, err := a.svc.GetPrice(r.Context(), r.PathValue("skuId"))
	if err != nil {
		writePriceError(w, r, err)
		return
	}
	response.JSON(w, http.StatusOK, "Price", view, nil)
}

func writePriceError(w http.ResponseWriter, r *http.Request, err error) {
	var validation *domain.ValidationError
	switch {
	case errors.As(err, &validation):
		response.Error(w, r, http.StatusBadRequest, "VALIDATION_FAILED", "The request has invalid fields", validation.Fields)
	case errors.Is(err, domain.ErrSKUNotFound), errors.Is(err, domain.ErrProductNotFound):
		response.Error(w, r, http.StatusNotFound, "SKU_NOT_FOUND", "SKU not found", nil)
	case dependency.IsUnavailable(err):
		dependency.WriteUnavailable(w, r, err)
	default:
		log.Printf("[catalog] price request failed: %v", err)
		response.Error(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal server error", nil)
	}
}
