package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/request"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/promotion"
)

func registerPromotionRoutes(mux *http.ServeMux, store promotion.Store) {
	if store == nil {
		return
	}
	adminGuard := func(h http.HandlerFunc) http.Handler {
		return auth.RequireAuth(auth.RequireRole(RoleMarketing, "OPS_MGR", "SUPER_ADMIN", "ACCESS_ADMIN")(h))
	}

	// Storefront: list active campaigns (public) and evaluate eligible campaigns (auth)
	mux.HandleFunc("GET /api/v1/promotions/active", func(w http.ResponseWriter, r *http.Request) {
		items, err := store.ListActive(r.Context(), time.Now().UTC())
		if err != nil {
			writePromoError(w, r, err)
			return
		}
		response.JSON(w, http.StatusOK, "Active promotions", items, nil)
	})

	mux.Handle("POST /api/v1/promotions/evaluate", auth.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			CampaignIDs []string             `json:"campaignIds"`
			Lines       []promotion.EvalLine `json:"lines"`
		}
		if !request.DecodeJSON(w, r, &body) {
			return
		}
		claims, _ := auth.ClaimsFrom(r.Context())
		now := time.Now().UTC()
		active, err := store.ListActive(r.Context(), now)
		if err != nil {
			writePromoError(w, r, err)
			return
		}
		ids := make([]string, 0, len(active))
		for _, c := range active {
			ids = append(ids, c.CampaignID)
		}
		uses, err := store.CustomerUsesMap(r.Context(), claims.UserID, ids)
		if err != nil {
			writePromoError(w, r, err)
			return
		}
		isNew, err := store.IsNewCustomer(r.Context(), claims.UserID)
		if err != nil {
			writePromoError(w, r, err)
			return
		}
		res := promotion.Evaluate(active, uses, isNew, promotion.EvalInput{
			UserID:      claims.UserID,
			CampaignIDs: body.CampaignIDs,
			Lines:       body.Lines,
		}, now)
		response.JSON(w, http.StatusOK, "Promotion evaluation", res, nil)
	})))

	// Admin CRUD for Marketing
	mux.Handle("GET /api/v1/admin/promotions", adminGuard(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		items, err := store.List(r.Context(), strings.TrimSpace(q.Get("status")), strings.TrimSpace(q.Get("q")))
		if err != nil {
			writePromoError(w, r, err)
			return
		}
		response.JSON(w, http.StatusOK, "Promotion campaigns", items, nil)
	}))

	mux.Handle("GET /api/v1/admin/promotions/{campaignId}", adminGuard(func(w http.ResponseWriter, r *http.Request) {
		c, err := store.Get(r.Context(), r.PathValue("campaignId"))
		if err != nil {
			writePromoError(w, r, err)
			return
		}
		response.JSON(w, http.StatusOK, "Promotion campaign", c, nil)
	}))

	mux.Handle("POST /api/v1/admin/promotions", adminGuard(func(w http.ResponseWriter, r *http.Request) {
		var body promotion.Fields
		if !request.DecodeJSON(w, r, &body) {
			return
		}
		claims, _ := auth.ClaimsFrom(r.Context())
		if body.Status == "" {
			body.Status = promotion.StatusActive
		}
		if body.PerCustomerLimit <= 0 {
			body.PerCustomerLimit = 1
		}
		if body.Priority <= 0 {
			body.Priority = 100
		}
		c, err := store.Create(r.Context(), newPromoID(), claims.UserID, body)
		if err != nil {
			writePromoError(w, r, err)
			return
		}
		response.JSON(w, http.StatusCreated, "Promotion campaign created", c, nil)
	}))

	updateHandler := adminGuard(func(w http.ResponseWriter, r *http.Request) {
		var body promotion.Fields
		if !request.DecodeJSON(w, r, &body) {
			return
		}
		c, err := store.Update(r.Context(), r.PathValue("campaignId"), body)
		if err != nil {
			writePromoError(w, r, err)
			return
		}
		response.JSON(w, http.StatusOK, "Promotion campaign updated", c, nil)
	})
	mux.Handle("PUT /api/v1/admin/promotions/{campaignId}", updateHandler)
	mux.Handle("PATCH /api/v1/admin/promotions/{campaignId}", updateHandler)
}

func newPromoID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return "CAMP-" + strings.ToUpper(hex.EncodeToString(b[:]))
}

func writePromoError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case dependency.IsUnavailable(err):
		dependency.WriteUnavailable(w, r, err)
	case errors.Is(err, promotion.ErrNotFound):
		response.Error(w, r, http.StatusNotFound, "CAMPAIGN_NOT_FOUND", err.Error(), nil)
	case errors.Is(err, promotion.ErrCodeExists):
		response.Error(w, r, http.StatusConflict, "CAMPAIGN_CODE_EXISTS", err.Error(), nil)
	case errors.Is(err, promotion.ErrBudgetExceeded), errors.Is(err, promotion.ErrQuotaExhausted):
		response.Error(w, r, http.StatusConflict, "CAMPAIGN_EXHAUSTED", err.Error(), nil)
	default:
		response.Error(w, r, http.StatusUnprocessableEntity, "VALIDATION_FAILED", err.Error(), nil)
	}
}
