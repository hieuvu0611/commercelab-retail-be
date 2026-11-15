package main

import (
	"context"
	"log"
	"net/http"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/config"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/db"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/middleware"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/migrate"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/s3"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/cache"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/cart"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/domain"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/inventory"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/peer"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/repository"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/storage"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/migrations"
)

// Deps are the optional collaborators of the catalog handler. Without Categories or Products the matching
// endpoints use an in-memory store (development and tests); without Cache nothing is cached.
type Deps struct {
	Categories *domain.CategoryService
	Products   *domain.ProductService
	Prices     *domain.PriceService
	Images     *domain.ImageService
	Storefront *domain.StorefrontService
	Cache      cache.Cache
	// Carts defaults to an in-memory store (development and tests).
	Carts cart.Store
	// Vouchers and Addresses are the order and IAM services the quote asks; without them a quote request that
	// needs one answers 503.
	Vouchers  VoucherChecker
	Addresses AddressChecker
	// Availability supplies stock states for the storefront; nil reports every availability as unknown.
	Availability domain.AvailabilityLookup
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
		deps.Storefront = domain.NewStorefrontService(reads, deps.Categories, repo).WithAvailability(deps.Availability)
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
	if deps.Carts == nil {
		deps.Carts = cart.NewMemoryStore()
	}
	cartSvc := cart.NewService(deps.Carts, repo)
	registerCartRoutes(mux, cartSvc)

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		response.JSON(w, http.StatusOK, "catalog-pricing-service healthy", map[string]any{
			"service":        "catalog-pricing-service",
			"postgresSchema": "pricing",
			"mongoColl":      "catalog_products",
			"pgConnected":    pgConnected,
			"mongoConnected": mongoConnected,
		}, nil)
	})

	registerQuoteRoutes(mux, repo, cartSvc, deps.Vouchers, deps.Addresses)

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
		deps.Carts = cart.NewRedisStore(redisClient)
	}

	repo := repository.NewCatalogPricingRepository(pgPool, mongoStore)
	deps.Vouchers = peer.NewVouchers(config.EnvOrDefault("ORDER_SERVICE_URL", "http://localhost:8083"), nil)
	deps.Addresses = peer.NewAddresses(config.EnvOrDefault("IAM_SERVICE_URL", "http://localhost:8087"), nil)
	deps.Availability = inventory.NewClient(config.EnvOrDefault("INVENTORY_SERVICE_URL", "http://localhost:8082"), nil)
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
