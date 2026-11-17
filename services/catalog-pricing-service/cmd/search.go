package main

import (
	"context"
	"log"
	"net/http"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/domain"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/search"
)

type openSearchAdapter struct {
	client *search.Client
}

func (a *openSearchAdapter) SearchProducts(ctx context.Context, q domain.StorefrontQuery, categoryIDs []string) ([]domain.PublishedRef, any, bool) {
	if a == nil || a.client == nil {
		return nil, nil, false
	}
	res, err := a.client.Search(ctx, search.Query{
		Text:        q.Q,
		CategoryIDs: categoryIDs,
		MinPrice:    q.MinPrice,
		MaxPrice:    q.MaxPrice,
	})
	if err != nil {
		log.Printf("[catalog] opensearch query fallback to mongodb: %v", err)
		return nil, nil, false
	}
	if len(res.ProductIDs) == 0 && q.Q == "" && q.CategoryPath == "" && q.MinPrice == nil && q.MaxPrice == nil {
		// Unindexed empty index fallback so clean boots still show catalog before first reindex.
		return nil, nil, false
	}
	refs := make([]domain.PublishedRef, len(res.ProductIDs))
	for i, id := range res.ProductIDs {
		refs[i] = domain.PublishedRef{ID: id}
	}
	return refs, res.Facets, true
}

func registerSearchAdminRoutes(mux *http.ServeMux, store domain.StorefrontStore, prices domain.PriceLookup, client *search.Client) {
	adminGuard := func(h http.HandlerFunc) http.Handler {
		return auth.RequireAuth(auth.RequireRole("PRODUCT_STAFF", "MARKETING", "OPS_MGR", "ADMIN", "ACCESS_ADMIN")(h))
	}

	mux.Handle("POST /api/v1/admin/search/reindex", adminGuard(func(w http.ResponseWriter, r *http.Request) {
		if client == nil {
			response.Error(w, r, http.StatusServiceUnavailable, "OPENSEARCH_NOT_CONFIGURED", "OPENSEARCH_URL is not configured", nil)
			return
		}
		if store == nil || prices == nil {
			response.Error(w, r, http.StatusServiceUnavailable, "CATALOG_UNAVAILABLE", "Catalog store is unavailable", nil)
			return
		}
		refs, err := store.FindPublished(r.Context(), "", nil)
		if err != nil {
			writeStorefrontError(w, r, err)
			return
		}
		ids := make([]string, len(refs))
		for i, ref := range refs {
			ids[i] = ref.ID
		}
		products, err := store.PublishedByIDs(r.Context(), ids)
		if err != nil {
			writeStorefrontError(w, r, err)
			return
		}
		skus, err := store.ActiveSKUs(r.Context(), ids, nil)
		if err != nil {
			writeStorefrontError(w, r, err)
			return
		}
		skuIDs := make([]string, len(skus))
		for i, sk := range skus {
			skuIDs[i] = sk.SkuID
		}
		priceMap, err := prices.OrderablePrices(r.Context(), skuIDs)
		if err != nil {
			writeStorefrontError(w, r, err)
			return
		}
		skusByProd := map[string][]string{}
		minPriceByProd := map[string]int64{}
		maxPriceByProd := map[string]int64{}
		for _, sk := range skus {
			skusByProd[sk.ProductID] = append(skusByProd[sk.ProductID], sk.SkuID)
			if p, ok := priceMap[sk.SkuID]; ok {
				if cur, exists := minPriceByProd[sk.ProductID]; !exists || p.UnitPriceVND < cur {
					minPriceByProd[sk.ProductID] = p.UnitPriceVND
				}
				if p.UnitPriceVND > maxPriceByProd[sk.ProductID] {
					maxPriceByProd[sk.ProductID] = p.UnitPriceVND
				}
			}
		}

		docs := make([]search.ProductDoc, 0, len(products))
		for _, prod := range products {
			pubAt := prod.UpdatedAt
			if prod.PublishedAt != nil {
				pubAt = *prod.PublishedAt
			}
			docs = append(docs, search.ProductDoc{
				ID:                prod.ID,
				Slug:              prod.Slug,
				Name:              prod.Name,
				Description:       prod.Description,
				Brand:             prod.Brand,
				PrimaryCategoryID: prod.PrimaryCategoryID,
				CategoryIDs:       prod.CategoryIDs,
				MinPriceVND:       minPriceByProd[prod.ID],
				MaxPriceVND:       maxPriceByProd[prod.ID],
				SkuIDs:            skusByProd[prod.ID],
				PublishedAt:       pubAt,
			})
		}

		indexed, err := client.BulkIndex(r.Context(), docs)
		if err != nil {
			response.Error(w, r, http.StatusBadGateway, "OPENSEARCH_REINDEX_FAILED", err.Error(), nil)
			return
		}
		response.JSON(w, http.StatusOK, "OpenSearch catalog reindexed", map[string]any{
			"indexedCount": indexed,
			"indexName":    client.IndexName(),
		}, nil)
	}))
}
