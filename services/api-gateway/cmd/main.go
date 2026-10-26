package main

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"

	"github.com/hieuvu0211/full-service-shopping/be/pkg/config"
	"github.com/hieuvu0211/full-service-shopping/be/pkg/middleware"
	"github.com/hieuvu0211/full-service-shopping/be/pkg/response"
)

// CartItem biểu diễn món hàng trong Giỏ (P02: Giỏ không giữ tồn kho)
type CartItem struct {
	SkuID    string `json:"skuId"`
	Quantity int64  `json:"quantity"`
}

type Cart struct {
	UserID string     `json:"userId"`
	Items  []CartItem `json:"items"`
	Note   string     `json:"note"`
}

var (
	cartMu    sync.RWMutex
	cartStore = map[string]Cart{
		"user-lan": {
			UserID: "user-lan",
			Items: []CartItem{
				{SkuID: "SKU-BALO-BLACK-20L", Quantity: 2},
				{SkuID: "SKU-BOTTLE-BLUE-500ML", Quantity: 1},
			},
			Note: "Giỏ hàng tạm trên Redis — Chưa giữ tồn kho (Policy P02)",
		},
	}
)

func main() {
	port := config.EnvOrDefault("API_GATEWAY_PORT", "8080")
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		response.JSON(w, http.StatusOK, "api-gateway healthy", map[string]any{
			"service":   "api-gateway",
			"port":      port,
			"redisAddr": config.EnvOrDefault("REDIS_ADDR", "localhost:6379"),
		}, nil)
	})

	// P02: Quản lý giỏ hàng tạm (Chưa hứa giữ hàng)
	mux.HandleFunc("GET /api/v1/carts/{userId}", func(w http.ResponseWriter, r *http.Request) {
		userID := r.PathValue("userId")
		cartMu.RLock()
		cart, ok := cartStore[userID]
		cartMu.RUnlock()
		if !ok {
			cart = Cart{UserID: userID, Items: []CartItem{}, Note: "Giỏ hàng tạm — Chưa giữ tồn kho (P02)"}
		}
		response.JSON(w, http.StatusOK, "Lấy giỏ hàng thành công", cart, nil)
	})

	mux.HandleFunc("PUT /api/v1/carts/{userId}", func(w http.ResponseWriter, r *http.Request) {
		userID := r.PathValue("userId")
		var req struct {
			Items []CartItem `json:"items"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			response.Error(w, r, http.StatusBadRequest, "INVALID_PAYLOAD", "Dữ liệu giỏ hàng không hợp lệ", nil)
			return
		}
		for _, item := range req.Items {
			if item.Quantity <= 0 {
				response.Error(w, r, http.StatusBadRequest, "INVALID_QUANTITY", "Số lượng phải lớn hơn 0", nil)
				return
			}
		}
		cart := Cart{
			UserID: userID,
			Items:  req.Items,
			Note:   "Giỏ hàng tạm trên Redis — Chưa giữ tồn kho (Policy P02)",
		}
		cartMu.Lock()
		cartStore[userID] = cart
		cartMu.Unlock()
		response.JSON(w, http.StatusOK, "Cập nhật giỏ hàng thành công", cart, nil)
	})

	log.Printf("[api-gateway] listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, middleware.Chain(mux)))
}
