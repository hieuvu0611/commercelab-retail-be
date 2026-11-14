package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

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
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Email           string   `json:"email"`
	Role            string   `json:"role"`
	WarehouseScope  string   `json:"warehouseScope"`
	AccessToken     string   `json:"accessToken,omitempty"`
	RefreshToken    string   `json:"refreshToken,omitempty"`
	Roles           []string `json:"roles"`
	WarehouseScopes []string `json:"warehouseScopes"`
}

// UserStore is what the HTTP handlers need from the user repository.
type UserStore interface {
	Authenticate(ctx context.Context, email, password string) (repository.User, error)
	FindByID(ctx context.Context, id string) (repository.User, error)
	Register(ctx context.Context, email, password, fullName string) (repository.User, error)
}

// issueSession starts a new refresh-token family for u, signs an access token and returns the profile
// the clients receive. It writes the error response itself and returns false on failure.
func issueSession(w http.ResponseWriter, r *http.Request, tokens TokenStore, u repository.User) (UserProfile, bool) {
	accessTok, err := auth.SignAccessToken(u.Identity())
	if err != nil {
		response.Error(w, r, http.StatusInternalServerError, "TOKEN_SIGN_ERROR", "Could not create the access token", nil)
		return UserProfile{}, false
	}
	tokenID, errID := repository.NewTokenID("rt-")
	familyID, errFam := repository.NewTokenID("fam-")
	if errID != nil || errFam != nil {
		response.Error(w, r, http.StatusInternalServerError, "TOKEN_SIGN_ERROR", "Could not create the refresh token", nil)
		return UserProfile{}, false
	}
	refreshTok, rec, err := signRefresh(u, tokenID, familyID)
	if err != nil {
		response.Error(w, r, http.StatusInternalServerError, "TOKEN_SIGN_ERROR", "Could not create the refresh token", nil)
		return UserProfile{}, false
	}
	if err := tokens.Create(r.Context(), rec, repository.HashToken(refreshTok)); err != nil {
		if dependency.IsUnavailable(err) {
			dependency.WriteUnavailable(w, r, err)
		} else {
			response.Error(w, r, http.StatusInternalServerError, "TOKEN_STORE_ERROR", "Could not create the session", nil)
		}
		return UserProfile{}, false
	}
	return UserProfile{
		ID:              u.ID,
		Name:            u.Name,
		Email:           u.Email,
		Role:            u.Role,
		WarehouseScope:  u.WarehouseScope,
		Roles:           u.Identity().Roles,
		WarehouseScopes: u.Identity().WarehouseScopes,
		AccessToken:     accessTok,
		RefreshToken:    refreshTok,
	}, true
}

// TokenStore persists hashed refresh tokens (implemented by repository.RefreshRepository).
type TokenStore interface {
	Create(ctx context.Context, rec repository.RefreshTokenRecord, tokenHash string) error
	Rotate(ctx context.Context, presentedHash string, next repository.RefreshTokenRecord, nextHash string) (repository.RefreshTokenRecord, error)
	RevokeFamilyOf(ctx context.Context, tokenHash string) error
}

// refreshTokenTTL is how long a refresh token stays valid (D-03: 7 days).
const refreshTokenTTL = 7 * 24 * time.Hour

// signRefresh signs a refresh token for u with the given token id and returns it with its stored record.
func signRefresh(u repository.User, tokenID, familyID string) (string, repository.RefreshTokenRecord, error) {
	token, err := auth.SignRefreshToken(u.ID, u.Role, u.WarehouseScope, tokenID)
	if err != nil {
		return "", repository.RefreshTokenRecord{}, err
	}
	return token, repository.RefreshTokenRecord{
		ID: tokenID, UserID: u.ID, FamilyID: familyID, ExpiresAt: time.Now().UTC().Add(refreshTokenTTL),
	}, nil
}

func NewServer(repo UserStore, tokens TokenStore, dbConnected bool) http.Handler {
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
		profile, ok := issueSession(w, r, tokens, u)
		if !ok {
			return
		}
		response.JSON(w, http.StatusOK, "Đăng nhập thành công", profile, nil)
	})

	// P1-T03 (D-02): customers self-register with email + password; the role is always CUSTOMER.
	mux.HandleFunc("POST /api/v1/auth/register", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Email    string `json:"email"`
			Password string `json:"password"`
			FullName string `json:"fullName"`
		}
		if !request.DecodeJSON(w, r, &req) {
			return
		}
		email := normalizeEmail(req.Email)
		if fieldErrors := validateRegistration(email, req.Password, req.FullName); len(fieldErrors) != 0 {
			response.Error(w, r, http.StatusUnprocessableEntity, "VALIDATION_FAILED", "Request validation failed", fieldErrors)
			return
		}
		u, err := repo.Register(r.Context(), email, req.Password, strings.TrimSpace(req.FullName))
		if dependency.IsUnavailable(err) {
			dependency.WriteUnavailable(w, r, err)
			return
		}
		if errors.Is(err, repository.ErrEmailTaken) {
			response.Error(w, r, http.StatusConflict, "EMAIL_ALREADY_REGISTERED", "An account with this email already exists", nil)
			return
		}
		if err != nil {
			response.Error(w, r, http.StatusInternalServerError, "REGISTER_FAILED", "Could not create the account", nil)
			return
		}
		profile, ok := issueSession(w, r, tokens, u)
		if !ok {
			return
		}
		response.JSON(w, http.StatusCreated, "Account created", profile, nil)
	})

	// Refresh rotates the refresh token (D-03). Reusing an already-rotated token revokes the whole family.
	refresh := func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			RefreshToken string `json:"refreshToken"`
		}
		if !request.DecodeJSON(w, r, &req) {
			return
		}
		if req.RefreshToken == "" {
			response.Error(w, r, http.StatusUnauthorized, "INVALID_REFRESH_TOKEN", "Refresh token is invalid", nil)
			return
		}
		claims, err := auth.ParseRefreshToken(req.RefreshToken)
		if err != nil {
			response.Error(w, r, http.StatusUnauthorized, "INVALID_REFRESH_TOKEN", "Refresh token is invalid or expired", nil)
			return
		}
		u, err := repo.FindByID(r.Context(), claims.UserID)
		if dependency.IsUnavailable(err) {
			dependency.WriteUnavailable(w, r, err)
			return
		}
		if err != nil || !u.IsActive {
			_ = tokens.RevokeFamilyOf(r.Context(), repository.HashToken(req.RefreshToken))
			response.Error(w, r, http.StatusUnauthorized, "USER_INACTIVE", "The account does not exist or is disabled", nil)
			return
		}
		nextID, err := repository.NewTokenID("rt-")
		if err != nil {
			response.Error(w, r, http.StatusInternalServerError, "TOKEN_SIGN_ERROR", "Could not create the refresh token", nil)
			return
		}
		nextToken, nextRec, err := signRefresh(u, nextID, "")
		if err != nil {
			response.Error(w, r, http.StatusInternalServerError, "TOKEN_SIGN_ERROR", "Could not create the refresh token", nil)
			return
		}
		_, err = tokens.Rotate(r.Context(), repository.HashToken(req.RefreshToken), nextRec, repository.HashToken(nextToken))
		switch {
		case dependency.IsUnavailable(err):
			dependency.WriteUnavailable(w, r, err)
			return
		case errors.Is(err, repository.ErrRefreshReused):
			response.Error(w, r, http.StatusUnauthorized, "REFRESH_TOKEN_REUSED", "Refresh token was already used; sign in again", nil)
			return
		case errors.Is(err, repository.ErrRefreshExpired):
			response.Error(w, r, http.StatusUnauthorized, "REFRESH_TOKEN_EXPIRED", "Refresh token has expired; sign in again", nil)
			return
		case err != nil:
			response.Error(w, r, http.StatusUnauthorized, "INVALID_REFRESH_TOKEN", "Refresh token is invalid", nil)
			return
		}
		accessTok, err := auth.SignAccessToken(u.Identity())
		if err != nil {
			response.Error(w, r, http.StatusInternalServerError, "TOKEN_SIGN_ERROR", "Could not create the access token", nil)
			return
		}
		response.JSON(w, http.StatusOK, "Token refreshed", map[string]string{
			"accessToken":  accessTok,
			"refreshToken": nextToken,
		}, nil)
	}
	mux.HandleFunc("POST /api/v1/auth/refresh", refresh)
	// Deprecated alias kept for one phase: the frontend contract (docs/04 6.3) still calls POST /auth/refresh.
	mux.HandleFunc("POST /auth/refresh", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Deprecation", "true")
		refresh(w, r)
	})

	// Logout revokes the whole refresh-token family of the presented token. It is idempotent.
	mux.HandleFunc("POST /api/v1/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			RefreshToken string `json:"refreshToken"`
		}
		if !request.DecodeJSON(w, r, &req) {
			return
		}
		if req.RefreshToken == "" {
			response.Error(w, r, http.StatusUnprocessableEntity, "VALIDATION_FAILED", "Request validation failed",
				map[string][]string{"refreshToken": {"Refresh token is required"}})
			return
		}
		if err := tokens.RevokeFamilyOf(r.Context(), repository.HashToken(req.RefreshToken)); err != nil {
			if dependency.IsUnavailable(err) {
				dependency.WriteUnavailable(w, r, err)
				return
			}
			response.Error(w, r, http.StatusInternalServerError, "LOGOUT_FAILED", "Could not sign out", nil)
			return
		}
		response.JSON(w, http.StatusOK, "Signed out", map[string]bool{"revoked": true}, nil)
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
			ID:              u.ID,
			Name:            u.Name,
			Email:           u.Email,
			Role:            u.Role,
			WarehouseScope:  u.WarehouseScope,
			Roles:           u.Identity().Roles,
			WarehouseScopes: u.Identity().WarehouseScopes,
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
	log.Fatal(http.ListenAndServe(":"+port, NewServer(repo, repository.NewRefreshRepository(repo), true)))
}
