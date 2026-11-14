package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/config"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/db"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/middleware"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/request"
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

// Get returns the cart for userID. With Redis configured, any Redis failure other than a missing key
// is reported as an unavailable dependency; the in-memory store is only used without Redis
// (ALLOW_MEMORY_FALLBACK=true).
func (s *CartStorage) Get(ctx context.Context, userID string) (Cart, error) {
	if s.redisClient != nil {
		raw, err := s.redisClient.Get(ctx, "cart:"+userID).Bytes()
		if errors.Is(err, redis.Nil) {
			return emptyCart(userID), nil
		}
		if err != nil {
			return Cart{}, dependency.Wrap(err)
		}
		var c Cart
		if err := json.Unmarshal(raw, &c); err != nil {
			return Cart{}, dependency.Wrap(err)
		}
		return c, nil
	}
	s.mu.RLock()
	cart, ok := s.memStore[userID]
	s.mu.RUnlock()
	if !ok {
		return emptyCart(userID), nil
	}
	return cart, nil
}

func emptyCart(userID string) Cart {
	return Cart{UserID: userID, Items: []CartItem{}, Note: "Giỏ hàng tạm — Chưa giữ tồn kho (P02)"}
}

// Put stores the cart. With Redis configured, a write failure is an unavailable dependency.
func (s *CartStorage) Put(ctx context.Context, cart Cart) error {
	if s.redisClient != nil {
		raw, err := json.Marshal(cart)
		if err != nil {
			return err
		}
		if err := s.redisClient.Set(ctx, "cart:"+cart.UserID, raw, 7*24*time.Hour).Err(); err != nil {
			return dependency.Wrap(err)
		}
		return nil
	}
	s.mu.Lock()
	s.memStore[cart.UserID] = cart
	s.mu.Unlock()
	return nil
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

	// P02: the temporary cart. It always belongs to the subject of the access token; the user id is never
	// taken from the URL or the body, so one user cannot read or write another user's cart.
	mux.Handle("GET /api/v1/carts/me", auth.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := auth.ClaimsFrom(r.Context())
		cart, err := cartStorage.Get(r.Context(), claims.UserID)
		if err != nil {
			dependency.WriteUnavailable(w, r, err)
			return
		}
		response.JSON(w, http.StatusOK, "Lấy giỏ hàng thành công", cart, nil)
	})))

	mux.Handle("PUT /api/v1/carts/me", auth.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := auth.ClaimsFrom(r.Context())
		var req struct {
			Items []CartItem `json:"items"`
		}
		if !request.DecodeJSON(w, r, &req) {
			return
		}
		for _, item := range req.Items {
			if item.Quantity <= 0 {
				response.Error(w, r, http.StatusBadRequest, "INVALID_QUANTITY", "Số lượng phải lớn hơn 0", nil)
				return
			}
		}
		cart := Cart{
			UserID: claims.UserID,
			Items:  req.Items,
			Note:   "Giỏ hàng tạm trên Redis — Chưa giữ tồn kho (Policy P02)",
		}
		if err := cartStorage.Put(r.Context(), cart); err != nil {
			dependency.WriteUnavailable(w, r, err)
			return
		}
		response.JSON(w, http.StatusOK, "Cập nhật giỏ hàng thành công", cart, nil)
	})))

	// Every other API goes to the downstream services. Only the public allowlist may skip authentication;
	// anything else needs a valid access token, which the service then verifies again itself.
	authenticatedProxy := auth.RequireAuth(proxyRouter)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if isPublic(r.Method, r.URL.Path) {
			proxyRouter.ServeHTTP(w, r)
			return
		}
		authenticatedProxy.ServeHTTP(w, r)
	})

	return middleware.Chain(mux)
}

// Config is the typed start-up configuration of the API gateway.
type Config struct {
	Port      string
	RedisAddr string
	RedisPass string
}

// loadConfig reads and validates the environment, including the mandatory JWT secrets.
// It also installs the secrets into pkg/auth. Every problem is reported in one error.
func loadConfig() (Config, error) {
	l := config.NewLoader()
	auth.LoadSecrets(l)
	cfg := Config{
		Port:      l.Optional("API_GATEWAY_PORT", "8080"),
		RedisAddr: l.Optional("REDIS_ADDR", "localhost:6379"),
		RedisPass: l.Optional("REDIS_PASSWORD", ""),
	}
	return cfg, l.Err()
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("[api-gateway] %v", err)
	}
	port, redisAddr, redisPass := cfg.Port, cfg.RedisAddr, cfg.RedisPass

	redisClient, err := db.ConnectRedis(context.Background(), redisAddr, redisPass)
	if err := dependency.CheckStartup("api-gateway", "redis", err); err != nil {
		log.Fatal(err)
	}
	if redisClient != nil {
		defer func() { _ = redisClient.Close() }()
	}

	cartStorage := NewCartStorage(redisClient)
	proxyRouter := proxy.NewRouter(proxy.DefaultTargetsFromEnv())

	log.Printf("[api-gateway] listening on :%s (redisConnected: %v)", port, redisClient != nil)
	log.Fatal(http.ListenAndServe(":"+port, NewServer(cartStorage, proxyRouter, redisClient != nil)))
}
