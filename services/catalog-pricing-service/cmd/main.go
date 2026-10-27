package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/hieuvu0211/full-service-shopping/be/pkg/config"
	"github.com/hieuvu0211/full-service-shopping/be/pkg/db"
	"github.com/hieuvu0211/full-service-shopping/be/pkg/middleware"
	"github.com/hieuvu0211/full-service-shopping/be/pkg/response"
	"github.com/hieuvu0211/full-service-shopping/be/services/catalog-pricing-service/internal/domain"
	"github.com/hieuvu0211/full-service-shopping/be/services/catalog-pricing-service/internal/repository"
)

var quoteSeq uint64 = 1

func NewServer(repo *repository.CatalogPricingRepository, pgConnected, mongoConnected bool) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		response.JSON(w, http.StatusOK, "catalog-pricing-service healthy", map[string]any{
			"service":        "catalog-pricing-service",
			"postgresSchema": "pricing",
			"mongoColl":      "catalog_products",
			"pgConnected":    pgConnected,
			"mongoConnected": mongoConnected,
		}, nil)
	})

	// B01: Lấy danh sách sản phẩm từ Catalog (MongoDB polymorphic specs + Postgres SKU prices)
	mux.HandleFunc("GET /api/v1/products", func(w http.ResponseWriter, r *http.Request) {
		products, err := repo.ListPublishedProducts(r.Context())
		if err != nil {
			response.Error(w, r, http.StatusInternalServerError, "CATALOG_FETCH_FAILED", err.Error(), nil)
			return
		}
		response.JSON(w, http.StatusOK, "Lấy danh mục sản phẩm thành công", products, &response.Metadata{
			Total:     int64(len(products)),
			Page:      1,
			PageCount: 1,
			Limit:     10,
		})
	})

	// P02, P08, SC01: Tạo báo giá có hiệu lực 5 phút & lưu vào pricing.quotes
	mux.HandleFunc("POST /api/v1/quotes", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			QuoteID     string                  `json:"quoteId"`
			UserID      string                  `json:"userId"`
			VoucherCode string                  `json:"voucherCode"`
			Items       []domain.QuoteLineInput `json:"items"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			response.Error(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Dữ liệu yêu cầu báo giá không hợp lệ", nil)
			return
		}
		if req.UserID == "" {
			req.UserID = "user-lan"
		}
		for i := range req.Items {
			if req.Items[i].UnitPriceVND <= 0 || req.Items[i].SkuName == "" {
				sp, err := repo.GetSkuPrice(r.Context(), req.Items[i].SkuID)
				if err != nil {
					response.Error(w, r, http.StatusNotFound, "SKU_NOT_FOUND", err.Error()+": "+req.Items[i].SkuID, nil)
					return
				}
				req.Items[i].UnitPriceVND = sp.UnitPriceVND
				req.Items[i].SkuName = sp.SkuName
			}
		}

		var discountPct int64
		if req.VoucherCode == "WELCOME10" {
			discountPct = 10
		}
		quoteID := req.QuoteID
		if quoteID == "" {
			seq := atomic.AddUint64(&quoteSeq, 1)
			quoteID = fmt.Sprintf("Q-%s-%03d", time.Now().UTC().Format("150405"), seq)
		}
		quote, err := domain.CalculateQuote(quoteID, req.UserID, req.VoucherCode, discountPct, req.Items, time.Now().UTC())
		if err != nil {
			response.Error(w, r, http.StatusBadRequest, "QUOTE_CALCULATION_FAILED", err.Error(), nil)
			return
		}
		if err := repo.SaveQuote(r.Context(), quote); err != nil {
			response.Error(w, r, http.StatusInternalServerError, "QUOTE_PERSIST_FAILED", err.Error(), nil)
			return
		}
		response.JSON(w, http.StatusCreated, "Tạo báo giá 5 phút thành công", quote, nil)
	})

	// P02: Truy vấn báo giá theo quoteId kèm kiểm tra hiệu lực 5 phút
	mux.HandleFunc("GET /api/v1/quotes/{quoteId}", func(w http.ResponseWriter, r *http.Request) {
		quoteID := r.PathValue("quoteId")
		quote, err := repo.GetQuoteByID(r.Context(), quoteID)
		if err != nil {
			response.Error(w, r, http.StatusNotFound, "QUOTE_NOT_FOUND", err.Error(), nil)
			return
		}
		isExpired := time.Now().UTC().After(quote.ExpiresAt)
		response.JSON(w, http.StatusOK, "Lấy thông tin báo giá thành công", map[string]any{
			"quote":     quote,
			"isExpired": isExpired,
		}, nil)
	})

	return middleware.Chain(mux)
}

func main() {
	port := config.EnvOrDefault("CATALOG_PRICING_PORT", "8081")
	pgURL := config.EnvOrDefault("DATABASE_URL_PRICING", "postgres://pricing_svc_user:pricing_secret@localhost:5432/commerce_lab?sslmode=disable&search_path=pricing")
	mongoURI := config.EnvOrDefault("MONGO_URI", "mongodb://mongo_admin:mongo_password@localhost:27017/commerce_lab_doc?authSource=admin")
	mongoDB := config.EnvOrDefault("MONGO_DATABASE", "commerce_lab_doc")

	ctx := context.Background()
	pgPool, pgErr := db.ConnectPostgres(ctx, pgURL)
	if pgErr != nil {
		log.Printf("[catalog-pricing-service] postgres unavailable (%v), using seed fallback", pgErr)
	} else {
		defer pgPool.Close()
	}

	_, mongoStore, mongoErr := db.ConnectMongo(ctx, mongoURI, mongoDB)
	if mongoErr != nil {
		log.Printf("[catalog-pricing-service] mongodb unavailable (%v), using seed fallback", mongoErr)
	}

	repo := repository.NewCatalogPricingRepository(pgPool, mongoStore)
	log.Printf("[catalog-pricing-service] listening on :%s (schema: pricing, mongo: catalog_products)", port)
	log.Fatal(http.ListenAndServe(":"+port, NewServer(repo, pgPool != nil, mongoStore != nil)))
}
