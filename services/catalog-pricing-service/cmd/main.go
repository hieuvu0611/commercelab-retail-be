package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/config"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/db"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/middleware"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/migrate"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/s3"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/cache"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/domain"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/repository"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/storage"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/migrations"
)

var quoteSeq uint64 = 1

// Deps are the optional collaborators of the catalog handler. Without Categories or Products the matching
// endpoints use an in-memory store (development and tests); without Cache nothing is cached.
type Deps struct {
	Categories *domain.CategoryService
	Products   *domain.ProductService
	Prices     *domain.PriceService
	Images     *domain.ImageService
	Storefront *domain.StorefrontService
	Cache      cache.Cache
}

func NewServer(repo *repository.CatalogPricingRepository, pgConnected, mongoConnected bool) http.Handler {
	return NewServerWithDeps(repo, pgConnected, mongoConnected, Deps{})
}

func NewServerWithDeps(repo *repository.CatalogPricingRepository, pgConnected, mongoConnected bool, deps Deps) http.Handler {
	mux := http.NewServeMux()
	if deps.Categories == nil {
		deps.Categories = domain.NewCategoryService(domain.NewMemoryCategoryStore())
	}
	memoryProducts := domain.NewMemoryProductStore()
	if deps.Products == nil {
		deps.Products = domain.NewProductService(memoryProducts, deps.Categories).WithPricing(repo, repo)
	}
	if deps.Storefront == nil {
		var reads domain.StorefrontStore = memoryProducts
		if mongoStore := repo.MongoStore(); mongoStore != nil {
			reads = repository.NewMongoStorefrontStore(mongoStore)
		}
		deps.Storefront = domain.NewStorefrontService(reads, deps.Categories, repo)
	}
	registerCategoryRoutes(mux, deps.Categories, deps.Cache)
	if deps.Images == nil {
		deps.Images = domain.NewImageService(deps.Products, nil)
	}
	registerProductAdminRoutes(mux, deps.Products, deps.Images)
	registerStorefrontRoutes(mux, deps.Storefront, deps.Images)
	if deps.Prices == nil {
		deps.Prices = domain.NewPriceService(deps.Products, repo)
	}
	registerPriceRoutes(mux, deps.Prices)

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		response.JSON(w, http.StatusOK, "catalog-pricing-service healthy", map[string]any{
			"service":        "catalog-pricing-service",
			"postgresSchema": "pricing",
			"mongoColl":      "catalog_products",
			"pgConnected":    pgConnected,
			"mongoConnected": mongoConnected,
		}, nil)
	})

	// P02, P08, SC01: Tạo báo giá có hiệu lực 5 phút & lưu vào pricing.quotes
	mux.HandleFunc("POST /api/v1/quotes", func(w http.ResponseWriter, r *http.Request) {
		// Prices and names are never taken from the client: any unitPriceVnd or skuName it sends is ignored.
		var req struct {
			QuoteID     string `json:"quoteId"`
			UserID      string `json:"userId"`
			VoucherCode string `json:"voucherCode"`
			Items       []struct {
				SkuID    string `json:"skuId"`
				Quantity int64  `json:"quantity"`
			} `json:"items"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			response.Error(w, r, http.StatusBadRequest, "INVALID_REQUEST", "The quote request is invalid", nil)
			return
		}
		if req.UserID == "" {
			req.UserID = "user-lan"
		}
		lines := make([]domain.QuoteLineInput, len(req.Items))
		for i, item := range req.Items {
			sp, err := repo.GetSkuPrice(r.Context(), item.SkuID)
			switch {
			case dependency.IsUnavailable(err):
				dependency.WriteUnavailable(w, r, err)
				return
			case errors.Is(err, repository.ErrSkuNotFound):
				response.Error(w, r, http.StatusNotFound, "SKU_NOT_FOUND", "SKU not found: "+item.SkuID, nil)
				return
			case errors.Is(err, repository.ErrSkuNotOrderable):
				response.Error(w, r, http.StatusConflict, "SKU_NOT_ORDERABLE", "SKU cannot be ordered right now: "+item.SkuID, nil)
				return
			case err != nil:
				log.Printf("[catalog] quote price lookup failed: %v", err)
				response.Error(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal server error", nil)
				return
			}
			lines[i] = domain.QuoteLineInput{SkuID: sp.SkuID, SkuName: sp.SkuName, Quantity: item.Quantity, UnitPriceVND: sp.UnitPriceVND}
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
		quote, err := domain.CalculateQuote(quoteID, req.UserID, req.VoucherCode, discountPct, lines, time.Now().UTC())
		if err != nil {
			response.Error(w, r, http.StatusBadRequest, "QUOTE_CALCULATION_FAILED", err.Error(), nil)
			return
		}
		if err := repo.SaveQuote(r.Context(), quote); err != nil {
			if dependency.IsUnavailable(err) {
				dependency.WriteUnavailable(w, r, err)
				return
			}
			response.Error(w, r, http.StatusInternalServerError, "QUOTE_PERSIST_FAILED", err.Error(), nil)
			return
		}
		response.JSON(w, http.StatusCreated, "Tạo báo giá 5 phút thành công", quote, nil)
	})

	// P02: Truy vấn báo giá theo quoteId kèm kiểm tra hiệu lực 5 phút
	mux.HandleFunc("GET /api/v1/quotes/{quoteId}", func(w http.ResponseWriter, r *http.Request) {
		quoteID := r.PathValue("quoteId")
		quote, err := repo.GetQuoteByID(r.Context(), quoteID)
		if dependency.IsUnavailable(err) {
			dependency.WriteUnavailable(w, r, err)
			return
		}
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

	redisAddr := config.EnvOrDefault("REDIS_ADDR", "localhost:6379")
	redisPass := config.EnvOrDefault("REDIS_PASSWORD", "")

	secretsLoader := config.NewLoader()
	auth.LoadSecrets(secretsLoader)
	if err := secretsLoader.Err(); err != nil {
		log.Fatalf("[catalog-pricing-service] %v", err)
	}

	ctx := context.Background()
	if migrate.EnabledOnStart() {
		if _, err := migrate.Up(ctx, pgURL, migrations.FS); err != nil {
			log.Fatalf("[catalog-pricing-service] migrations failed: %v", err)
		}
	}

	pgPool, pgErr := db.ConnectPostgres(ctx, pgURL)
	if err := dependency.CheckStartup("catalog-pricing-service", "postgres", pgErr); err != nil {
		log.Fatal(err)
	}
	if pgPool != nil {
		defer pgPool.Close()
	}

	_, mongoStore, mongoErr := db.ConnectMongo(ctx, mongoURI, mongoDB)
	if err := dependency.CheckStartup("catalog-pricing-service", "mongodb", mongoErr); err != nil {
		log.Fatal(err)
	}
	if mongoStore != nil {
		defer func() { _ = mongoStore.Close(context.Background()) }()
	}

	deps := Deps{}
	if mongoStore != nil {
		categoryStore, err := repository.NewMongoCategoryStore(ctx, mongoStore)
		if err != nil {
			log.Fatalf("[catalog-pricing-service] category store: %v", err)
		}
		deps.Categories = domain.NewCategoryService(categoryStore)
		productStore, err := repository.NewMongoProductStore(ctx, mongoStore)
		if err != nil {
			log.Fatalf("[catalog-pricing-service] product store: %v", err)
		}
		deps.Products = domain.NewProductService(productStore, deps.Categories)
	}
	// The category cache is an optimisation: without Redis (dev/test fallback only) nothing is cached.
	redisClient, redisErr := db.ConnectRedis(ctx, redisAddr, redisPass)
	if err := dependency.CheckStartup("catalog-pricing-service", "redis", redisErr); err != nil {
		log.Fatal(err)
	}
	if redisClient != nil {
		defer func() { _ = redisClient.Close() }()
		deps.Cache = cache.NewRedis(redisClient)
	}

	repo := repository.NewCatalogPricingRepository(pgPool, mongoStore)
	if deps.Products != nil {
		deps.Products.WithPricing(repo, repo)
		deps.Prices = domain.NewPriceService(deps.Products, repo)
		s3Client := s3.NewFromEnv()
		if config.EnvOrDefault("S3_AUTO_CREATE_BUCKET", "") == "true" {
			if err := s3Client.EnsureBucket(ctx); err != nil {
				log.Fatalf("[catalog-pricing-service] %v", err)
			}
		}
		deps.Images = domain.NewImageService(deps.Products, storage.NewS3Images(s3Client))
	}
	log.Printf("[catalog-pricing-service] listening on :%s (schema: pricing, mongo: catalog_products)", port)
	log.Fatal(http.ListenAndServe(":"+port, NewServerWithDeps(repo, pgPool != nil, mongoStore != nil, deps)))
}
