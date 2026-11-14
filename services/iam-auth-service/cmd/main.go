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
	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/middleware"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/migrate"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/request"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
	"github.com/hieuvu0611/commercelab-retail-be/services/iam-auth-service/internal/repository"
	"github.com/hieuvu0611/commercelab-retail-be/services/iam-auth-service/migrations"
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

// UserStore is what the HTTP handlers need from the user repository.
type UserStore interface {
	Authenticate(ctx context.Context, email, password string) (repository.User, error)
	FindByID(ctx context.Context, id string) (repository.User, error)
}

func NewServer(repo UserStore, dbConnected bool) http.Handler {
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
		if !request.DecodeJSON(w, r, &req) {
			return
		}
		fieldErrors := map[string][]string{}
		if req.Email == "" {
			fieldErrors["email"] = []string{"Email is required"}
		}
		if req.Password == "" {
			fieldErrors["password"] = []string{"Password is required"}
		}
		if len(fieldErrors) != 0 {
			response.Error(w, r, http.StatusUnprocessableEntity, "VALIDATION_FAILED", "Request validation failed", fieldErrors)
			return
		}
		u, err := repo.Authenticate(r.Context(), req.Email, req.Password)
		if dependency.IsUnavailable(err) {
			dependency.WriteUnavailable(w, r, err)
			return
		}
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
		if dependency.IsUnavailable(err) {
			dependency.WriteUnavailable(w, r, err)
			return
		}
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
		if dependency.IsUnavailable(err) {
			dependency.WriteUnavailable(w, r, err)
			return
		}
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

// Config is the typed start-up configuration of the IAM service.
type Config struct {
	Port        string
	DatabaseURL string
}

// loadConfig reads and validates the environment, including the mandatory JWT secrets.
// It also installs the secrets into pkg/auth. Every problem is reported in one error.
func loadConfig() (Config, error) {
	l := config.NewLoader()
	auth.LoadSecrets(l)
	cfg := Config{
		Port:        l.Optional("IAM_AUTH_PORT", "8087"),
		DatabaseURL: l.Optional("DATABASE_URL_IAM", "postgres://iam_svc_user:iam_secret@localhost:5432/commerce_lab?sslmode=disable&search_path=iam"),
	}
	return cfg, l.Err()
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("[iam-auth-service] %v", err)
	}
	port, dbURL := cfg.Port, cfg.DatabaseURL

	if migrate.EnabledOnStart() {
		if _, err := migrate.Up(context.Background(), dbURL, migrations.FS); err != nil {
			log.Fatalf("[iam-auth-service] migrations failed: %v", err)
		}
	}

	// IAM has no in-memory fallback: it never serves users that are not in Postgres,
	// so ALLOW_MEMORY_FALLBACK does not apply here.
	pool, err := db.ConnectPostgres(context.Background(), dbURL)
	if err != nil {
		log.Fatalf("[iam-auth-service] postgres is required and unavailable: %v", err)
	}
	defer pool.Close()

	repo := repository.NewUserRepository(pool)
	log.Printf("[iam-auth-service] listening on :%s (schema: iam)", port)
	log.Fatal(http.ListenAndServe(":"+port, NewServer(repo, true)))
}
