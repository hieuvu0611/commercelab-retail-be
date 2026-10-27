package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/config"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/db"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/middleware"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
	"github.com/hieuvu0611/commercelab-retail-be/services/api-gateway/internal/proxy"
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

type CartStorage struct {
	redisClient *redis.Client
	mu          sync.RWMutex
	memStore    map[string]Cart
}

func NewCartStorage(redisClient *redis.Client) *CartStorage {
	return &CartStorage{
		redisClient: redisClient,
		memStore: map[string]Cart{
			"user-lan": {
				UserID: "user-lan",
				Items: []CartItem{
					{SkuID: "SKU-BALO-BLACK-20L", Quantity: 2},
					{SkuID: "SKU-BOTTLE-BLUE-500ML", Quantity: 1},
				},
				Note: "Giỏ hàng tạm trên Redis — Chưa giữ tồn kho (Policy P02)",
			},
		},
	}
}

func (s *CartStorage) Get(ctx context.Context, userID string) Cart {
	if s.redisClient != nil {
		raw, err := s.redisClient.Get(ctx, "cart:"+userID).Bytes()
		if err == nil {
			var c Cart
			if json.Unmarshal(raw, &c) == nil {
				return c
			}
		}
	}
	s.mu.RLock()
	cart, ok := s.memStore[userID]
	s.mu.RUnlock()
	if !ok {
		return Cart{UserID: userID, Items: []CartItem{}, Note: "Giỏ hàng tạm — Chưa giữ tồn kho (P02)"}
	}
	return cart
}

func (s *CartStorage) Put(ctx context.Context, cart Cart) {
	if s.redisClient != nil {
		if raw, err := json.Marshal(cart); err == nil {
			_ = s.redisClient.Set(ctx, "cart:"+cart.UserID, raw, 7*24*time.Hour).Err()
		}
	}
	s.mu.Lock()
	s.memStore[cart.UserID] = cart
	s.mu.Unlock()
}

func NewServer(cartStorage *CartStorage, proxyRouter *proxy.Router, redisConnected bool) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		response.JSON(w, http.StatusOK, "api-gateway healthy", map[string]any{
			"service":        "api-gateway",
			"redisAddr":      config.EnvOrDefault("REDIS_ADDR", "localhost:6379"),
			"redisConnected": redisConnected,
		}, nil)
	})

	// P02: Quản lý giỏ hàng tạm (Chưa hứa giữ hàng)
	mux.HandleFunc("GET /api/v1/carts/{userId}", func(w http.ResponseWriter, r *http.Request) {
		userID := r.PathValue("userId")
		cart := cartStorage.Get(r.Context(), userID)
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
		cartStorage.Put(r.Context(), cart)
		response.JSON(w, http.StatusOK, "Cập nhật giỏ hàng thành công", cart, nil)
	})

	// Định tuyến toàn bộ các API còn lại xuống các downstream microservices (:8081 - :8089)
	mux.Handle("/", proxyRouter)

	return middleware.Chain(mux)
}

func main() {
	port := config.EnvOrDefault("API_GATEWAY_PORT", "8080")
	redisAddr := config.EnvOrDefault("REDIS_ADDR", "localhost:6379")
	redisPass := config.EnvOrDefault("REDIS_PASSWORD", "")

	redisClient, err := db.ConnectRedis(context.Background(), redisAddr, redisPass)
	if err != nil {
		log.Printf("[api-gateway] redis unavailable (%v), using in-memory cart fallback", err)
	} else {
		defer func() { _ = redisClient.Close() }()
	}

	cartStorage := NewCartStorage(redisClient)
	proxyRouter := proxy.NewRouter(proxy.DefaultTargetsFromEnv())

	log.Printf("[api-gateway] listening on :%s (redisConnected: %v)", port, redisClient != nil)
	log.Fatal(http.ListenAndServe(":"+port, NewServer(cartStorage, proxyRouter, redisClient != nil)))
}
