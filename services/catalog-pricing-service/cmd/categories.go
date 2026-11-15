package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/request"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/cache"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/domain"
)

const (
	// RoleProductStaff may manage the catalog (categories, products, SKUs, publishing).
	RoleProductStaff = "PRODUCT_STAFF"

	categoryTreeCacheKey = "catalog:categories:tree:v1"
	categoryTreeCacheTTL = 5 * time.Minute
)

// categoryAPI serves the public category tree and the PRODUCT_STAFF administration endpoints.
type categoryAPI struct {
	svc   *domain.CategoryService
	cache cache.Cache // optional
}

func registerCategoryRoutes(mux *http.ServeMux, svc *domain.CategoryService, c cache.Cache) {
	api := &categoryAPI{svc: svc, cache: c}
	staff := func(h http.HandlerFunc) http.Handler {
		return auth.RequireAuth(auth.RequireRole(RoleProductStaff)(h))
	}
	mux.HandleFunc("GET /api/v1/categories", api.publicTree)
	mux.HandleFunc("GET /api/v1/categories/{path...}", api.publicByPath)
	mux.Handle("GET /api/v1/admin/categories", staff(api.adminList))
	mux.Handle("POST /api/v1/admin/categories", staff(api.create))
	mux.Handle("PATCH /api/v1/admin/categories/{id}", staff(api.update))
	mux.Handle("POST /api/v1/admin/categories/{id}/move", staff(api.move))
	mux.Handle("DELETE /api/v1/admin/categories/{id}", staff(api.remove))
}

// tree returns the active tree from the cache, rebuilding it on a miss. Cache problems are logged and never
// fail the request: the database stays the source of truth.
func (a *categoryAPI) tree(w http.ResponseWriter, r *http.Request) ([]domain.CategoryNode, bool) {
	ctx := r.Context()
	if a.cache != nil {
		raw, found, err := a.cache.Get(ctx, categoryTreeCacheKey)
		if err != nil {
			log.Printf("[catalog] category cache read failed: %v", err)
		} else if found {
			var cached []domain.CategoryNode
			if json.Unmarshal(raw, &cached) == nil {
				return cached, true
			}
			log.Printf("[catalog] category cache holds invalid data; rebuilding")
		}
	}
	cats, err := a.svc.List(ctx)
	if err != nil {
		writeCategoryError(w, r, err)
		return nil, false
	}
	tree := domain.BuildCategoryTree(cats)
	if a.cache != nil {
		if raw, err := json.Marshal(tree); err == nil {
			if err := a.cache.Set(ctx, categoryTreeCacheKey, raw, categoryTreeCacheTTL); err != nil {
				log.Printf("[catalog] category cache write failed: %v", err)
			}
		}
	}
	return tree, true
}

func (a *categoryAPI) invalidate(r *http.Request) {
	if a.cache == nil {
		return
	}
	if err := a.cache.Delete(r.Context(), categoryTreeCacheKey); err != nil {
		log.Printf("[catalog] category cache invalidation failed: %v", err)
	}
}

func (a *categoryAPI) publicTree(w http.ResponseWriter, r *http.Request) {
	tree, ok := a.tree(w, r)
	if !ok {
		return
	}
	response.JSON(w, http.StatusOK, "Categories", tree, nil)
}

// adminList returns every category, inactive ones included, as a flat list (the storefront tree hides them).
func (a *categoryAPI) adminList(w http.ResponseWriter, r *http.Request) {
	cats, err := a.svc.List(r.Context())
	if err != nil {
		writeCategoryError(w, r, err)
		return
	}
	if cats == nil {
		cats = []domain.Category{}
	}
	response.JSON(w, http.StatusOK, "Categories", cats, nil)
}

func (a *categoryAPI) publicByPath(w http.ResponseWriter, r *http.Request) {
	tree, ok := a.tree(w, r)
	if !ok {
		return
	}
	node, found := domain.FindCategoryNode(tree, "/"+r.PathValue("path"))
	if !found {
		writeCategoryError(w, r, domain.ErrCategoryNotFound)
		return
	}
	response.JSON(w, http.StatusOK, "Category", node, nil)
}

func (a *categoryAPI) create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ParentID  string `json:"parentId"`
		Name      string `json:"name"`
		Slug      string `json:"slug"`
		SortOrder int    `json:"sortOrder"`
	}
	if !request.DecodeJSON(w, r, &body) {
		return
	}
	c, err := a.svc.Create(r.Context(), domain.CreateCategoryInput{
		ParentID: body.ParentID, Name: body.Name, Slug: body.Slug, SortOrder: body.SortOrder,
	})
	a.finish(w, r, http.StatusCreated, "Category created", c, err)
}

func (a *categoryAPI) update(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name      *string `json:"name"`
		Slug      *string `json:"slug"`
		SortOrder *int    `json:"sortOrder"`
		IsActive  *bool   `json:"isActive"`
	}
	if !request.DecodeJSON(w, r, &body) {
		return
	}
	c, err := a.svc.Update(r.Context(), r.PathValue("id"), domain.CategoryPatch{
		Name: body.Name, Slug: body.Slug, SortOrder: body.SortOrder, IsActive: body.IsActive,
	})
	a.finish(w, r, http.StatusOK, "Category updated", c, err)
}

func (a *categoryAPI) move(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ParentID *string `json:"parentId"` // null or "" moves the category to the root
	}
	if !request.DecodeJSON(w, r, &body) {
		return
	}
	parentID := ""
	if body.ParentID != nil {
		parentID = *body.ParentID
	}
	c, err := a.svc.Move(r.Context(), r.PathValue("id"), parentID)
	a.finish(w, r, http.StatusOK, "Category moved", c, err)
}

func (a *categoryAPI) remove(w http.ResponseWriter, r *http.Request) {
	err := a.svc.Delete(r.Context(), r.PathValue("id"))
	a.finish(w, r, http.StatusNoContent, "", struct{}{}, err)
}

// finish invalidates the cache and writes the success response, or maps err to its error response.
func (a *categoryAPI) finish(w http.ResponseWriter, r *http.Request, status int, message string, data any, err error) {
	if err != nil {
		writeCategoryError(w, r, err)
		return
	}
	a.invalidate(r)
	response.JSON(w, status, message, data, nil)
}

func writeCategoryError(w http.ResponseWriter, r *http.Request, err error) {
	var validation *domain.ValidationError
	switch {
	case errors.As(err, &validation):
		response.Error(w, r, http.StatusBadRequest, "VALIDATION_FAILED", "The request has invalid fields", validation.Fields)
	case errors.Is(err, domain.ErrCategoryNotFound):
		response.Error(w, r, http.StatusNotFound, "CATEGORY_NOT_FOUND", "Category not found", nil)
	case errors.Is(err, domain.ErrCategoryDepthExceeded):
		response.Error(w, r, http.StatusConflict, "CATEGORY_DEPTH_EXCEEDED", err.Error(), nil)
	case errors.Is(err, domain.ErrCategoryCycle):
		response.Error(w, r, http.StatusConflict, "CATEGORY_CYCLE", err.Error(), nil)
	case errors.Is(err, domain.ErrCategoryHasChildren):
		response.Error(w, r, http.StatusConflict, "CATEGORY_HAS_CHILDREN", err.Error(), nil)
	case errors.Is(err, domain.ErrCategoryHasProducts):
		response.Error(w, r, http.StatusConflict, "CATEGORY_HAS_PRODUCTS", err.Error(), nil)
	case errors.Is(err, domain.ErrCategoryConflict):
		response.Error(w, r, http.StatusConflict, "CATEGORY_CONFLICT", err.Error(), nil)
	case dependency.IsUnavailable(err):
		dependency.WriteUnavailable(w, r, err)
	default:
		log.Printf("[catalog] category request failed: %v", err)
		response.Error(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal server error", nil)
	}
}
