package main

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/hieuvu0211/full-service-shopping/be/pkg/config"
	"github.com/hieuvu0211/full-service-shopping/be/pkg/middleware"
	"github.com/hieuvu0211/full-service-shopping/be/pkg/response"
	"github.com/hieuvu0211/full-service-shopping/be/services/catalog-pricing-service/internal/domain"
)

type ProductCatalogDoc struct {
	ProductID    string         `json:"productId"`
	Name         string         `json:"name"`
	CategoryType string         `json:"categoryType"`
	Status       string         `json:"status"`
	Specs        map[string]any `json:"specs"`
	Skus         []map[string]any `json:"skus"`
}

func main() {
	port := config.EnvOrDefault("CATALOG_PRICING_PORT", "8081")
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		response.JSON(w, http.StatusOK, "catalog-pricing-service healthy", map[string]any{
			"service":        "catalog-pricing-service",
			"postgresSchema": "pricing",
			"mongoColl":      "catalog_products",
		}, nil)
	})

	// B01: Lấy danh sách sản phẩm từ Catalog (MongoDB polymorphic specs)
	mux.HandleFunc("GET /api/v1/products", func(w http.ResponseWriter, r *http.Request) {
		products := []ProductCatalogDoc{
			{
				ProductID:    "PROD-BALO-CITY",
				Name:         "Balo City Chống Nước Đa Năng",
				CategoryType: "BACKPACK",
				Status:       "PUBLISHED",
				Specs: map[string]any{
					"capacityLiters":        20,
					"laptopCompartmentInch": 15.6,
					"material":              "Cordura Ballistic Nylon",
					"waterResistance":       "IPX4",
				},
				Skus: []map[string]any{
					{"skuId": "SKU-BALO-BLACK-20L", "skuName": "Balo City Đen 20L", "unitPriceVnd": 300000, "vatIncludedPct": 10},
				},
			},
			{
				ProductID:    "PROD-BOTTLE-ECO",
				Name:         "Bình Nước Giữ Nhiệt Eco",
				CategoryType: "THERMOS_BOTTLE",
				Status:       "PUBLISHED",
				Specs: map[string]any{
					"volumeMl":          500,
					"keepHotHours":      12,
					"keepColdHours":     24,
					"innerCoreMaterial": "Inox 316",
				},
				Skus: []map[string]any{
					{"skuId": "SKU-BOTTLE-BLUE-500ML", "skuName": "Bình Nước Xanh 500ml", "unitPriceVnd": 100000, "vatIncludedPct": 10},
				},
			},
		}
		response.JSON(w, http.StatusOK, "Lấy danh mục sản phẩm thành công", products, &response.Metadata{
			Total:     int64(len(products)),
			Page:      1,
			PageCount: 1,
			Limit:     10,
		})
	})

	// P02, P08, SC01: Tạo báo giá có hiệu lực 5 phút & phân bổ giảm giá xuống từng dòng
	mux.HandleFunc("POST /api/v1/quotes", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			UserID      string                  `json:"userId"`
			VoucherCode string                  `json:"voucherCode"`
			Items       []domain.QuoteLineInput `json:"items"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			response.Error(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Dữ liệu yêu cầu báo giá không hợp lệ", nil)
			return
		}
		var discountPct int64
		if req.VoucherCode == "WELCOME10" {
			discountPct = 10
		}
		quote, err := domain.CalculateQuote("Q-"+time.Now().UTC().Format("150405"), req.UserID, req.VoucherCode, discountPct, req.Items, time.Now().UTC())
		if err != nil {
			response.Error(w, r, http.StatusBadRequest, "QUOTE_CALCULATION_FAILED", err.Error(), nil)
			return
		}
		response.JSON(w, http.StatusCreated, "Tạo báo giá 5 phút thành công", quote, nil)
	})

	log.Printf("[catalog-pricing-service] listening on :%s (schema: pricing, mongo: catalog_products)", port)
	log.Fatal(http.ListenAndServe(":"+port, middleware.Chain(mux)))
}
