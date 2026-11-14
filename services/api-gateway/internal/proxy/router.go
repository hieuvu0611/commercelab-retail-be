package proxy

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/config"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
)

type ServiceTargets struct {
	IAM          string
	Catalog      string
	Inventory    string
	Order        string
	Payment      string
	Fulfillment  string
	Ledger       string
	Backoffice   string
	Notification string
}

func DefaultTargetsFromEnv() ServiceTargets {
	return ServiceTargets{
		IAM:          config.EnvOrDefault("IAM_SERVICE_URL", "http://localhost:8087"),
		Catalog:      config.EnvOrDefault("CATALOG_SERVICE_URL", "http://localhost:8081"),
		Inventory:    config.EnvOrDefault("INVENTORY_SERVICE_URL", "http://localhost:8082"),
		Order:        config.EnvOrDefault("ORDER_SERVICE_URL", "http://localhost:8083"),
		Payment:      config.EnvOrDefault("PAYMENT_SERVICE_URL", "http://localhost:8084"),
		Fulfillment:  config.EnvOrDefault("FULFILLMENT_SERVICE_URL", "http://localhost:8085"),
		Ledger:       config.EnvOrDefault("LEDGER_SERVICE_URL", "http://localhost:8086"),
		Backoffice:   config.EnvOrDefault("BACKOFFICE_SERVICE_URL", "http://localhost:8088"),
		Notification: config.EnvOrDefault("NOTIFICATION_SERVICE_URL", "http://localhost:8089"),
	}
}

type Router struct {
	targets   ServiceTargets
	Transport http.RoundTripper
}

func NewRouter(targets ServiceTargets) *Router {
	return &Router{targets: targets}
}

func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Identity headers are never trusted: services verify the Bearer token themselves, so the gateway
	// removes any X-User-* / X-Warehouse-Scope header a client sends and does not add its own.
	r.Header.Del("X-User-ID")
	r.Header.Del("X-User-Role")
	r.Header.Del("X-Warehouse-Scope")
	// The reverse proxy appends the real peer address to X-Forwarded-For; anything a client put there is dropped
	// first so services can trust the last entry (sign-in rate limiting relies on it).
	r.Header.Del("X-Forwarded-For")

	targetBase := rt.resolveTarget(r.URL.Path)
	if targetBase == "" {
		response.Error(w, r, http.StatusNotFound, "ROUTE_NOT_FOUND", "Không tìm thấy microservice đích cho đường dẫn "+r.URL.Path, nil)
		return
	}

	parsedURL, err := url.Parse(targetBase)
	if err != nil {
		response.Error(w, r, http.StatusInternalServerError, "INVALID_UPSTREAM_URL", err.Error(), nil)
		return
	}

	proxy := httputil.NewSingleHostReverseProxy(parsedURL)
	proxy.ModifyResponse = func(res *http.Response) error {
		// The gateway's correlation ID is authoritative for the client response.
		res.Header.Del("X-Correlation-ID")
		return nil
	}
	if rt.Transport != nil {
		proxy.Transport = rt.Transport
	}
	proxy.ErrorHandler = func(rw http.ResponseWriter, req *http.Request, proxyErr error) {
		response.Error(rw, req, http.StatusBadGateway, "UPSTREAM_UNAVAILABLE", "Microservice đích tạm thời không phản hồi: "+proxyErr.Error(), nil)
	}
	proxy.ServeHTTP(w, r)
}

func (rt *Router) resolveTarget(path string) string {
	switch {
	case path == "/auth/refresh" || strings.HasPrefix(path, "/api/v1/auth") || strings.HasPrefix(path, "/api/v1/admin"):
		return rt.targets.IAM
	case strings.HasPrefix(path, "/api/v1/products") || strings.HasPrefix(path, "/api/v1/categories") || strings.HasPrefix(path, "/api/v1/quotes"):
		return rt.targets.Catalog
	case strings.HasPrefix(path, "/api/v1/inventory"):
		return rt.targets.Inventory
	case strings.HasPrefix(path, "/api/v1/orders"):
		return rt.targets.Order
	case strings.HasPrefix(path, "/api/v1/payments"):
		return rt.targets.Payment
	case strings.HasPrefix(path, "/api/v1/shipments"):
		return rt.targets.Fulfillment
	case strings.HasPrefix(path, "/api/v1/reconciliations"):
		return rt.targets.Ledger
	case strings.HasPrefix(path, "/api/v1/backoffice"):
		return rt.targets.Backoffice
	case strings.HasPrefix(path, "/api/v1/notifications"):
		return rt.targets.Notification
	default:
		return ""
	}
}
