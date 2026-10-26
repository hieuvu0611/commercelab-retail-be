package main

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/hieuvu0211/full-service-shopping/be/pkg/config"
	"github.com/hieuvu0211/full-service-shopping/be/pkg/middleware"
	"github.com/hieuvu0211/full-service-shopping/be/pkg/response"
)

type UserProfile struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Email          string `json:"email"`
	Role           string `json:"role"`
	WarehouseScope string `json:"warehouseScope"`
	AccessToken    string `json:"accessToken,omitempty"`
	RefreshToken   string `json:"refreshToken,omitempty"`
}

func main() {
	port := config.EnvOrDefault("IAM_AUTH_PORT", "8087")
	dbURL := config.EnvOrDefault("DATABASE_URL_IAM", "postgres://iam_svc_user:iam_secret@localhost:5432/commerce_lab?sslmode=disable&search_path=iam")

	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		response.JSON(w, http.StatusOK, "iam-auth-service healthy", map[string]any{
			"service":        "iam-auth-service",
			"postgresSchema": "iam",
			"mongoColl":      "iam_audit_logs",
			"dbConfigured":   dbURL != "",
		}, nil)
	})

	mux.HandleFunc("POST /api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Email == "" {
			response.Error(w, r, http.StatusBadRequest, "INVALID_CREDENTIALS", "Email và mật khẩu là bắt buộc", nil)
			return
		}
		user := UserProfile{
			ID:             "user-lan",
			Name:           "Khách hàng Lan",
			Email:          req.Email,
			Role:           "CUSTOMER",
			WarehouseScope: "NONE",
			AccessToken:    "jwt-access-token-sample",
			RefreshToken:   "jwt-refresh-token-sample",
		}
		response.JSON(w, http.StatusOK, "Đăng nhập thành công", user, nil)
	})

	// Khớp 100% với fe/src/configs/axios-config.ts (POST /auth/refresh)
	mux.HandleFunc("POST /auth/refresh", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			RefreshToken string `json:"refreshToken"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.RefreshToken == "" {
			response.Error(w, r, http.StatusUnauthorized, "INVALID_REFRESH_TOKEN", "Refresh token không hợp lệ", nil)
			return
		}
		response.JSON(w, http.StatusOK, "Làm mới token thành công", map[string]string{
			"accessToken":  "jwt-new-access-token",
			"refreshToken": "jwt-new-refresh-token",
		}, nil)
	})

	log.Printf("[iam-auth-service] listening on :%s (schema: iam)", port)
	log.Fatal(http.ListenAndServe(":"+port, middleware.Chain(mux)))
}
