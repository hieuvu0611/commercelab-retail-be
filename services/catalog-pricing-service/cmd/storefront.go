package main

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/domain"
)

type storefrontAPI struct {
	svc    *domain.StorefrontService
	images *domain.ImageService
}

func registerStorefrontRoutes(mux *http.ServeMux, svc *domain.StorefrontService, images *domain.ImageService) {
	api := &storefrontAPI{svc: svc, images: images}
	mux.HandleFunc("GET /api/v1/products", api.list)
	mux.HandleFunc("GET /api/v1/products/{slug}", api.detail)
}

// parseStorefrontQuery reads the query string; malformed numbers are reported as field errors.
func parseStorefrontQuery(r *http.Request) (domain.StorefrontQuery, map[string][]string) {
	v := r.URL.Query()
	q := domain.StorefrontQuery{CategoryPath: v.Get("category"), Q: v.Get("q"), Sort: v.Get("sort")}
	bad := map[string][]string{}
	intParam := func(name string) (int, bool) {
		raw := v.Get(name)
		if raw == "" {
			return 0, false
		}
		n, err := strconv.Atoi(raw)
		if err != nil {
			bad[name] = []string{name + " must be a whole number"}
			return 0, false
		}
		return n, true
	}
	priceParam := func(name string) *int64 {
		raw := v.Get(name)
		if raw == "" {
			return nil
		}
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			bad[name] = []string{name + " must be a whole number of VND"}
			return nil
		}
		return &n
	}
	q.MinPrice, q.MaxPrice = priceParam("minPrice"), priceParam("maxPrice")
	if n, ok := intParam("page"); ok {
		q.Page = n
	}
	if n, ok := intParam("limit"); ok {
		q.Limit = n
	}
	for key, values := range v {
		if axis, ok := strings.CutPrefix(key, "attr."); ok && len(values) > 0 {
			if q.Attrs == nil {
				q.Attrs = map[string][]string{}
			}
			// One request may list several values, comma separated (attr.color=Black,Navy): any of them matches.
			q.Attrs[axis] = strings.Split(strings.Join(values, ","), ",")
		}
	}
	return q, bad
}

func (a *storefrontAPI) list(w http.ResponseWriter, r *http.Request) {
	q, bad := parseStorefrontQuery(r)
	if len(bad) > 0 {
		response.Error(w, r, http.StatusBadRequest, "VALIDATION_FAILED", "The request has invalid fields", bad)
		return
	}
	page, err := a.svc.List(r.Context(), q)
	if err != nil {
		writeStorefrontError(w, r, err)
		return
	}
	for i := range page.Items {
		if page.Items[i].ImageKey != "" {
			page.Items[i].ImageURL = a.images.WithURLs(r.Context(), []domain.ProductImage{{Key: page.Items[i].ImageKey}})[0].URL
		}
	}
	response.JSON(w, http.StatusOK, "Products", page.Items, &response.Metadata{
		Total: page.Total, Page: page.Page, PageCount: page.PageCount, Limit: page.Limit,
		SearchEngine: page.SearchEngine, Facets: page.Facets,
	})
}

func (a *storefrontAPI) detail(w http.ResponseWriter, r *http.Request) {
	d, err := a.svc.Detail(r.Context(), r.PathValue("slug"))
	if err != nil {
		writeStorefrontError(w, r, err)
		return
	}
	d.Images = a.images.WithURLs(r.Context(), d.Images)
	response.JSON(w, http.StatusOK, "Product", d, nil)
}

func writeStorefrontError(w http.ResponseWriter, r *http.Request, err error) {
	var validation *domain.ValidationError
	switch {
	case errors.As(err, &validation):
		response.Error(w, r, http.StatusBadRequest, "VALIDATION_FAILED", "The request has invalid fields", validation.Fields)
	case errors.Is(err, domain.ErrCategoryNotFound):
		response.Error(w, r, http.StatusNotFound, "CATEGORY_NOT_FOUND", "Category not found", nil)
	case errors.Is(err, domain.ErrProductNotFound):
		response.Error(w, r, http.StatusNotFound, "PRODUCT_NOT_FOUND", "Product not found", nil)
	case dependency.IsUnavailable(err):
		dependency.WriteUnavailable(w, r, err)
	default:
		log.Printf("[catalog] storefront request failed: %v", err)
		response.Error(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal server error", nil)
	}
}
