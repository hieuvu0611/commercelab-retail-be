package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/config"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/db"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/middleware"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
	"github.com/hieuvu0611/commercelab-retail-be/services/iam-auth-service/internal/repository"
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

func NewServer(repo *repository.UserRepository, dbConnected bool) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		response.JSON(w, http.StatusOK, "iam-auth-service healthy", map[string]any{
			"service":        "iam-auth-service",
			"postgresSchema": "iam",
			"mongoColl":      "iam_audit_logs",
			"dbConnected":    dbConnected,
		}, nil)
	})

	mux.HandleFunc("POST /api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Email == "" || req.Password == "" {
			response.Error(w, r, http.StatusBadRequest, "INVALID_CREDENTIALS", "Email và mật khẩu là bắt buộc", nil)
			return
		}
		u, err := repo.Authenticate(r.Context(), req.Email, req.Password)
		if err != nil {
			response.Error(w, r, http.StatusUnauthorized, "INVALID_CREDENTIALS", err.Error(), nil)
			return
		}
		accessTok, err := auth.SignAccessToken(u.ID, u.Role, u.WarehouseScope)
		if err != nil {
			response.Error(w, r, http.StatusInternalServerError, "TOKEN_SIGN_ERROR", "Không thể tạo access token", nil)
			return
		}
		refreshTok, err := auth.SignRefreshToken(u.ID, u.Role, u.WarehouseScope)
		if err != nil {
			response.Error(w, r, http.StatusInternalServerError, "TOKEN_SIGN_ERROR", "Không thể tạo refresh token", nil)
			return
		}
		response.JSON(w, http.StatusOK, "Đăng nhập thành công", UserProfile{
			ID:             u.ID,
			Name:           u.Name,
			Email:          u.Email,
			Role:           u.Role,
			WarehouseScope: u.WarehouseScope,
			AccessToken:    accessTok,
			RefreshToken:   refreshTok,
		}, nil)
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
		claims, err := auth.ParseRefreshToken(req.RefreshToken)
		if err != nil {
			response.Error(w, r, http.StatusUnauthorized, "INVALID_REFRESH_TOKEN", "Refresh token hết hạn hoặc không hợp lệ", nil)
			return
		}
		u, err := repo.FindByID(r.Context(), claims.UserID)
		if err != nil || !u.IsActive {
			response.Error(w, r, http.StatusUnauthorized, "USER_INACTIVE", "Tài khoản không tồn tại hoặc đã khóa", nil)
			return
		}
		accessTok, _ := auth.SignAccessToken(u.ID, u.Role, u.WarehouseScope)
		refreshTok, _ := auth.SignRefreshToken(u.ID, u.Role, u.WarehouseScope)
		response.JSON(w, http.StatusOK, "Làm mới token thành công", map[string]string{
			"accessToken":  accessTok,
			"refreshToken": refreshTok,
		}, nil)
	})

	// GET /api/v1/auth/me: trả về thông tin người dùng hiện tại kèm role & warehouseScope (I08)
	mux.HandleFunc("GET /api/v1/auth/me", func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		tokenStr := strings.TrimPrefix(authHeader, "Bearer ")
		if tokenStr == "" || tokenStr == authHeader {
			response.Error(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "Thiếu Bearer token trong Authorization header", nil)
			return
		}
		claims, err := auth.ParseAccessToken(tokenStr)
		if err != nil {
			response.Error(w, r, http.StatusUnauthorized, "INVALID_ACCESS_TOKEN", "Access token không hợp lệ hoặc đã hết hạn", nil)
			return
		}
		u, err := repo.FindByID(r.Context(), claims.UserID)
		if err != nil {
			response.Error(w, r, http.StatusNotFound, "USER_NOT_FOUND", err.Error(), nil)
			return
		}
		response.JSON(w, http.StatusOK, "Lấy thông tin người dùng thành công", UserProfile{
			ID:             u.ID,
			Name:           u.Name,
			Email:          u.Email,
			Role:           u.Role,
			WarehouseScope: u.WarehouseScope,
		}, nil)
	})

	return middleware.Chain(mux)
}

func main() {
	port := config.EnvOrDefault("IAM_AUTH_PORT", "8087")
	dbURL := config.EnvOrDefault("DATABASE_URL_IAM", "postgres://iam_svc_user:iam_secret@localhost:5432/commerce_lab?sslmode=disable&search_path=iam")

	pool, err := db.ConnectPostgres(context.Background(), dbURL)
	if err != nil {
		log.Printf("[iam-auth-service] postgres unavailable (%v), using seed fallback repository", err)
	} else {
		defer pool.Close()
	}

	repo := repository.NewUserRepository(pool)
	log.Printf("[iam-auth-service] listening on :%s (schema: iam, dbConnected: %v)", port, pool != nil)
	log.Fatal(http.ListenAndServe(":"+port, NewServer(repo, pool != nil)))
}
