package main

import (
	"errors"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/request"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/domain"
)

// productAdminAPI serves the PRODUCT_STAFF product and SKU endpoints.
type productAdminAPI struct {
	svc *domain.ProductService
}

func registerProductAdminRoutes(mux *http.ServeMux, svc *domain.ProductService) {
	api := &productAdminAPI{svc: svc}
	staff := func(h http.HandlerFunc) http.Handler {
		return auth.RequireAuth(auth.RequireRole(RoleProductStaff)(h))
	}
	mux.Handle("POST /api/v1/admin/products", staff(api.create))
	mux.Handle("GET /api/v1/admin/products", staff(api.list))
	mux.Handle("GET /api/v1/admin/products/{id}", staff(api.get))
	mux.Handle("PATCH /api/v1/admin/products/{id}", staff(api.update))
	mux.Handle("POST /api/v1/admin/products/{id}/skus", staff(api.createSKU))
	mux.Handle("PATCH /api/v1/admin/products/{id}/skus/{skuId}", staff(api.updateSKU))
	mux.Handle("DELETE /api/v1/admin/products/{id}/skus/{skuId}", staff(api.deleteSKU))
}

func actorOf(r *http.Request) string {
	if claims, ok := auth.ClaimsFrom(r.Context()); ok {
		return claims.UserID
	}
	return ""
}

// expectedVersion reads the version the client edited: the If-Match header (quoted or plain) wins over the
// body's "version". It writes the error response itself and returns false when there is none or it is invalid.
func expectedVersion(w http.ResponseWriter, r *http.Request, bodyVersion *int64) (int64, bool) {
	raw := strings.TrimSpace(r.Header.Get("If-Match"))
	if raw == "" {
		if bodyVersion == nil {
			response.Error(w, r, http.StatusPreconditionRequired, "VERSION_REQUIRED", "Send the product version in the If-Match header or the version field", nil)
			return 0, false
		}
		raw = strconv.FormatInt(*bodyVersion, 10)
	}
	raw = strings.Trim(strings.TrimPrefix(raw, "W/"), `"`)
	version, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || version < 1 {
		response.Error(w, r, http.StatusBadRequest, "INVALID_VERSION", "The version must be a positive integer", nil)
		return 0, false
	}
	return version, true
}

func writeView(w http.ResponseWriter, status int, message string, v domain.ProductView) {
	w.Header().Set("ETag", `"`+strconv.FormatInt(v.Version, 10)+`"`)
	response.JSON(w, status, message, v, nil)
}

func (a *productAdminAPI) create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Slug              string         `json:"slug"`
		Name              string         `json:"name"`
		Description       string         `json:"description"`
		Brand             string         `json:"brand"`
		PrimaryCategoryID string         `json:"primaryCategoryId"`
		CategoryIDs       []string       `json:"categoryIds"`
		VariantAxes       []string       `json:"variantAxes"`
		Specs             map[string]any `json:"specs"`
	}
	if !request.DecodeJSON(w, r, &body) {
		return
	}
	v, err := a.svc.CreateProduct(r.Context(), actorOf(r), domain.ProductInput{
		Slug: body.Slug, Name: body.Name, Description: body.Description, Brand: body.Brand,
		PrimaryCategoryID: body.PrimaryCategoryID, CategoryIDs: body.CategoryIDs, VariantAxes: body.VariantAxes, Specs: body.Specs,
	})
	if err != nil {
		writeProductError(w, r, err)
		return
	}
	writeView(w, http.StatusCreated, "Product created", v)
}

func (a *productAdminAPI) get(w http.ResponseWriter, r *http.Request) {
	v, err := a.svc.GetProduct(r.Context(), r.PathValue("id"))
	if err != nil {
		writeProductError(w, r, err)
		return
	}
	writeView(w, http.StatusOK, "Product", v)
}

func queryInt(r *http.Request, name string, def int) (int, bool) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return def, true
	}
	n, err := strconv.Atoi(raw)
	return n, err == nil && n > 0
}

func (a *productAdminAPI) list(w http.ResponseWriter, r *http.Request) {
	page, okPage := queryInt(r, "page", 1)
	limit, okLimit := queryInt(r, "limit", 20)
	if !okPage || !okLimit {
		response.Error(w, r, http.StatusBadRequest, "VALIDATION_FAILED", "page and limit must be positive integers", nil)
		return
	}
	products, total, err := a.svc.ListProducts(r.Context(), domain.ProductFilter{
		Status: domain.ProductStatus(r.URL.Query().Get("status")), Q: strings.TrimSpace(r.URL.Query().Get("q")),
		Page: page, Limit: limit,
	})
	if err != nil {
		writeProductError(w, r, err)
		return
	}
	// The service caps the limit; report the effective one.
	if limit > 100 {
		limit = 100
	}
	response.JSON(w, http.StatusOK, "Products", products, &response.Metadata{
		Total: total, Page: page, Limit: limit, PageCount: int(math.Ceil(float64(total) / float64(limit))),
	})
}

func (a *productAdminAPI) update(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Version           *int64          `json:"version"`
		Slug              *string         `json:"slug"`
		Name              *string         `json:"name"`
		Description       *string         `json:"description"`
		Brand             *string         `json:"brand"`
		PrimaryCategoryID *string         `json:"primaryCategoryId"`
		CategoryIDs       *[]string       `json:"categoryIds"`
		VariantAxes       *[]string       `json:"variantAxes"`
		Specs             *map[string]any `json:"specs"`
	}
	if !request.DecodeJSON(w, r, &body) {
		return
	}
	version, ok := expectedVersion(w, r, body.Version)
	if !ok {
		return
	}
	v, err := a.svc.UpdateProduct(r.Context(), actorOf(r), r.PathValue("id"), version, domain.ProductPatch{
		Slug: body.Slug, Name: body.Name, Description: body.Description, Brand: body.Brand,
		PrimaryCategoryID: body.PrimaryCategoryID, CategoryIDs: body.CategoryIDs, VariantAxes: body.VariantAxes, Specs: body.Specs,
	})
	if err != nil {
		writeProductError(w, r, err)
		return
	}
	writeView(w, http.StatusOK, "Product updated", v)
}

func (a *productAdminAPI) createSKU(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Version    *int64            `json:"version"`
		SkuID      string            `json:"skuId"`
		Attributes map[string]string `json:"attributes"`
	}
	if !request.DecodeJSON(w, r, &body) {
		return
	}
	version, ok := expectedVersion(w, r, body.Version)
	if !ok {
		return
	}
	v, err := a.svc.CreateSKU(r.Context(), actorOf(r), r.PathValue("id"), version, domain.SKUInput{SkuID: body.SkuID, Attributes: body.Attributes})
	if err != nil {
		writeProductError(w, r, err)
		return
	}
	writeView(w, http.StatusCreated, "SKU created", v)
}

func (a *productAdminAPI) updateSKU(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Version    *int64            `json:"version"`
		Attributes map[string]string `json:"attributes"`
		IsActive   *bool             `json:"isActive"`
	}
	if !request.DecodeJSON(w, r, &body) {
		return
	}
	version, ok := expectedVersion(w, r, body.Version)
	if !ok {
		return
	}
	v, err := a.svc.UpdateSKU(r.Context(), actorOf(r), r.PathValue("id"), r.PathValue("skuId"), version,
		domain.SKUPatch{Attributes: body.Attributes, IsActive: body.IsActive})
	if err != nil {
		writeProductError(w, r, err)
		return
	}
	writeView(w, http.StatusOK, "SKU updated", v)
}

func (a *productAdminAPI) deleteSKU(w http.ResponseWriter, r *http.Request) {
	version, ok := expectedVersion(w, r, nil)
	if !ok {
		return
	}
	v, err := a.svc.DeleteSKU(r.Context(), actorOf(r), r.PathValue("id"), r.PathValue("skuId"), version)
	if err != nil {
		writeProductError(w, r, err)
		return
	}
	writeView(w, http.StatusOK, "SKU deleted", v)
}

func writeProductError(w http.ResponseWriter, r *http.Request, err error) {
	var validation *domain.ValidationError
	switch {
	case errors.As(err, &validation):
		response.Error(w, r, http.StatusBadRequest, "VALIDATION_FAILED", "The request has invalid fields", validation.Fields)
	case errors.Is(err, domain.ErrCategoryNotFound):
		response.Error(w, r, http.StatusBadRequest, "VALIDATION_FAILED", "The request has invalid fields",
			map[string][]string{"categoryIds": {"one of the categories does not exist"}})
	case errors.Is(err, domain.ErrProductNotFound):
		response.Error(w, r, http.StatusNotFound, "PRODUCT_NOT_FOUND", "Product not found", nil)
	case errors.Is(err, domain.ErrSKUNotFound):
		response.Error(w, r, http.StatusNotFound, "SKU_NOT_FOUND", "SKU not found", nil)
	case errors.Is(err, domain.ErrProductSlugTaken):
		response.Error(w, r, http.StatusConflict, "PRODUCT_SLUG_TAKEN", err.Error(), nil)
	case errors.Is(err, domain.ErrSKUCodeTaken):
		response.Error(w, r, http.StatusConflict, "SKU_CODE_TAKEN", err.Error(), nil)
	case errors.Is(err, domain.ErrVersionConflict):
		response.Error(w, r, http.StatusConflict, "VERSION_CONFLICT", "The product changed since you loaded it; reload and retry", nil)
	case errors.Is(err, domain.ErrProductNotEditable):
		response.Error(w, r, http.StatusConflict, "PRODUCT_NOT_EDITABLE", err.Error(), nil)
	case errors.Is(err, domain.ErrAxesLocked):
		response.Error(w, r, http.StatusConflict, "VARIANT_AXES_LOCKED", err.Error(), nil)
	case dependency.IsUnavailable(err):
		dependency.WriteUnavailable(w, r, err)
	default:
		log.Printf("[catalog] product request failed: %v", err)
		response.Error(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal server error", nil)
	}
}
