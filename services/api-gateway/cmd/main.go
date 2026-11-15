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
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
	"github.com/hieuvu0611/commercelab-retail-be/services/api-gateway/internal/proxy"
)

func NewServer(proxyRouter *proxy.Router, redisConnected bool) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		response.JSON(w, http.StatusOK, "api-gateway healthy", map[string]any{
			"service":        "api-gateway",
			"redisAddr":      config.EnvOrDefault("REDIS_ADDR", "localhost:6379"),
			"redisConnected": redisConnected,
		}, nil)
	})

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

	proxyRouter := proxy.NewRouter(proxy.DefaultTargetsFromEnv())

	log.Printf("[api-gateway] listening on :%s (redisConnected: %v)", port, redisClient != nil)
	log.Fatal(http.ListenAndServe(":"+port, NewServer(proxyRouter, redisClient != nil)))
}
