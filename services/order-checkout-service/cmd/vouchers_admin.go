package main

import (
	"errors"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/request"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/voucher"
)

// RoleMarketing manages voucher campaigns.
const RoleMarketing = "MARKETING"

type voucherAdminAPI struct {
	admin *voucher.Admin
}

// campaignJSON is a campaign as Marketing sees it, with the three quota buckets of P12.
type campaignJSON struct {
	Code             string     `json:"code"`
	Name             string     `json:"name"`
	DiscountPct      int        `json:"discountPct"`
	TotalQuota       int64      `json:"totalQuota"`
	AvailableQuota   int64      `json:"availableQuota"`
	ReservedQuota    int64      `json:"reservedQuota"`
	ConsumedQuota    int64      `json:"consumedQuota"`
	PerCustomerLimit int        `json:"perCustomerLimit"`
	StartsAt         *time.Time `json:"startsAt"`
	EndsAt           *time.Time `json:"endsAt"`
	Status           string     `json:"status"`
	Version          int64      `json:"version"`
	CreatedBy        string     `json:"createdBy"`
	CreatedAt        time.Time  `json:"createdAt"`
	UpdatedAt        time.Time  `json:"updatedAt"`
}

func toCampaignJSON(c voucher.Campaign) campaignJSON {
	return campaignJSON{
		Code: c.Code, Name: c.Name, DiscountPct: c.DiscountPct, TotalQuota: c.TotalQuota, AvailableQuota: c.Available(),
		ReservedQuota: c.ReservedQuota, ConsumedQuota: c.ConsumedQuota, PerCustomerLimit: c.PerCustomerLimit,
		StartsAt: c.StartsAt, EndsAt: c.EndsAt, Status: c.Status, Version: c.Version, CreatedBy: c.CreatedBy,
		CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}
}

// campaignBody is the request body of create and update. Optional fields are pointers so "absent" is known.
type campaignBody struct {
	Code             string     `json:"code"`
	Name             string     `json:"name"`
	DiscountPct      int        `json:"discountPct"`
	TotalQuota       int64      `json:"totalQuota"`
	PerCustomerLimit *int       `json:"perCustomerLimit"`
	StartsAt         *time.Time `json:"startsAt"`
	EndsAt           *time.Time `json:"endsAt"`
	Status           string     `json:"status"`
}

func (b campaignBody) fields() voucher.Fields {
	f := voucher.Fields{Name: b.Name, DiscountPct: b.DiscountPct, TotalQuota: b.TotalQuota, StartsAt: b.StartsAt, EndsAt: b.EndsAt, Status: b.Status}
	if b.PerCustomerLimit != nil {
		f.PerCustomerLimit = *b.PerCustomerLimit
	}
	return f
}

func registerVoucherAdminRoutes(mux *http.ServeMux, admin *voucher.Admin) {
	api := &voucherAdminAPI{admin: admin}
	marketing := func(h http.HandlerFunc) http.Handler {
		return auth.RequireAuth(auth.RequireRole(RoleMarketing)(h))
	}
	mux.Handle("POST /api/v1/admin/vouchers", marketing(api.create))
	mux.Handle("GET /api/v1/admin/vouchers", marketing(api.list))
	mux.Handle("GET /api/v1/admin/vouchers/{code}", marketing(api.get))
	mux.Handle("PUT /api/v1/admin/vouchers/{code}", marketing(api.update))
}

func (a *voucherAdminAPI) create(w http.ResponseWriter, r *http.Request) {
	var body campaignBody
	if !request.DecodeJSON(w, r, &body) {
		return
	}
	claims, _ := auth.ClaimsFrom(r.Context())
	c, err := a.admin.Create(r.Context(), body.Code, claims.UserID, body.fields())
	if err != nil {
		writeAdminError(w, r, err)
		return
	}
	response.JSON(w, http.StatusCreated, "Voucher campaign created", toCampaignJSON(c), nil)
}

func (a *voucherAdminAPI) update(w http.ResponseWriter, r *http.Request) {
	var body campaignBody
	if !request.DecodeJSON(w, r, &body) {
		return
	}
	if body.Code != "" && voucher.NormalizeCode(body.Code) != voucher.NormalizeCode(r.PathValue("code")) {
		response.Error(w, r, http.StatusBadRequest, "VALIDATION_FAILED", "The request has invalid fields",
			map[string][]string{"code": {"the code cannot be changed"}})
		return
	}
	c, err := a.admin.Update(r.Context(), r.PathValue("code"), body.fields())
	if err != nil {
		writeAdminError(w, r, err)
		return
	}
	response.JSON(w, http.StatusOK, "Voucher campaign updated", toCampaignJSON(c), nil)
}

func (a *voucherAdminAPI) get(w http.ResponseWriter, r *http.Request) {
	c, err := a.admin.Get(r.Context(), r.PathValue("code"))
	if err != nil {
		writeAdminError(w, r, err)
		return
	}
	response.JSON(w, http.StatusOK, "Voucher campaign", toCampaignJSON(c), nil)
}

func (a *voucherAdminAPI) list(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	filter := voucher.ListFilter{Q: query.Get("q"), Status: query.Get("status")}
	bad := map[string][]string{}
	if filter.Status != "" && filter.Status != voucher.StatusActive && filter.Status != voucher.StatusPaused {
		bad["status"] = []string{"status must be ACTIVE or PAUSED"}
	}
	for name, dst := range map[string]*int{"page": &filter.Page, "limit": &filter.Limit} {
		if raw := query.Get(name); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 {
				bad[name] = []string{name + " must be a positive whole number"}
				continue
			}
			*dst = n
		}
	}
	if len(bad) > 0 {
		response.Error(w, r, http.StatusBadRequest, "VALIDATION_FAILED", "The request has invalid fields", bad)
		return
	}
	campaigns, total, err := a.admin.List(r.Context(), filter)
	if err != nil {
		writeAdminError(w, r, err)
		return
	}
	page, limit := max(filter.Page, 1), filter.Limit
	if limit < 1 {
		limit = 20
	}
	limit = min(limit, 100)
	out := make([]campaignJSON, len(campaigns))
	for i, c := range campaigns {
		out[i] = toCampaignJSON(c)
	}
	response.JSON(w, http.StatusOK, "Voucher campaigns", out, &response.Metadata{
		Total: total, Page: page, Limit: limit, PageCount: int(math.Ceil(float64(total) / float64(limit))),
	})
}

func writeAdminError(w http.ResponseWriter, r *http.Request, err error) {
	var validation *voucher.ValidationError
	switch {
	case errors.As(err, &validation):
		response.Error(w, r, http.StatusBadRequest, "VALIDATION_FAILED", "The request has invalid fields", validation.Fields)
	case errors.Is(err, voucher.ErrNotFound):
		response.Error(w, r, http.StatusNotFound, "VOUCHER_NOT_FOUND", "Voucher campaign not found", nil)
	case errors.Is(err, voucher.ErrCodeExists):
		response.Error(w, r, http.StatusConflict, "VOUCHER_CODE_EXISTS", err.Error(), nil)
	case errors.Is(err, voucher.ErrInUse):
		response.Error(w, r, http.StatusConflict, "VOUCHER_IN_USE", err.Error(), nil)
	case errors.Is(err, voucher.ErrQuotaBelowUse):
		response.Error(w, r, http.StatusConflict, "VOUCHER_QUOTA_BELOW_USE", err.Error(), nil)
	case dependency.IsUnavailable(err):
		dependency.WriteUnavailable(w, r, err)
	default:
		writeVoucherError(w, r, err)
	}
}
