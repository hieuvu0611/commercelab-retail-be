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
	Phone           string   `json:"phone,omitempty"`
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
	UpdateProfile(ctx context.Context, userID string, update repository.ProfileUpdate) (repository.User, error)
	ChangePassword(ctx context.Context, userID, current, next string) error
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
	RevokeAllForUser(ctx context.Context, userID string) error
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

// NewServer builds the IAM handler without the administration API.
func NewServer(repo UserStore, tokens TokenStore, dbConnected bool) http.Handler {
	return NewServerWithAdmin(repo, tokens, nil, dbConnected)
}

// NewServerWithAdmin also serves the ACCESS_ADMIN staff administration API when admin is not nil.
func NewServerWithAdmin(repo UserStore, tokens TokenStore, admin AdminStore, dbConnected bool) http.Handler {
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

	profileOf := func(u repository.User) UserProfile {
		id := u.Identity()
		return UserProfile{
			ID:              u.ID,
			Name:            u.Name,
			Email:           u.Email,
			Phone:           u.Phone,
			Role:            u.Role,
			WarehouseScope:  u.WarehouseScope,
			Roles:           id.Roles,
			WarehouseScopes: id.WarehouseScopes,
		}
	}

	// writeLookupError answers for a failed user lookup of the authenticated subject.
	writeLookupError := func(w http.ResponseWriter, r *http.Request, err error) {
		if dependency.IsUnavailable(err) {
			dependency.WriteUnavailable(w, r, err)
			return
		}
		response.Error(w, r, http.StatusNotFound, "USER_NOT_FOUND", "The account does not exist", nil)
	}

	// GET /api/v1/auth/me: the signed-in user's profile with roles and warehouse scopes (I08).
	mux.Handle("GET /api/v1/auth/me", auth.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := auth.ClaimsFrom(r.Context())
		u, err := repo.FindByID(r.Context(), claims.UserID)
		if err != nil {
			writeLookupError(w, r, err)
			return
		}
		response.JSON(w, http.StatusOK, "Profile retrieved", profileOf(u), nil)
	})))

	// PATCH /api/v1/auth/me: the user may change only their own full name and phone number.
	mux.Handle("PATCH /api/v1/auth/me", auth.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := auth.ClaimsFrom(r.Context())
		var req struct {
			FullName *string `json:"fullName"`
			Phone    *string `json:"phone"`
		}
		if !request.DecodeJSON(w, r, &req) {
			return
		}
		fieldErrors := map[string][]string{}
		update := repository.ProfileUpdate{}
		if req.FullName != nil {
			name := strings.TrimSpace(*req.FullName)
			if problems := validateName(name); len(problems) != 0 {
				fieldErrors["fullName"] = problems
			}
			update.FullName = &name
		}
		if req.Phone != nil {
			phone := normalizePhone(*req.Phone)
			if phone != "" && !validPhone(phone) {
				fieldErrors["phone"] = []string{"Phone must be an international number of 7 to 15 digits"}
			}
			update.Phone = &phone
		}
		if req.FullName == nil && req.Phone == nil {
			fieldErrors["fullName"] = []string{"Send at least one of fullName or phone"}
		}
		if len(fieldErrors) != 0 {
			response.Error(w, r, http.StatusUnprocessableEntity, "VALIDATION_FAILED", "Request validation failed", fieldErrors)
			return
		}
		u, err := repo.UpdateProfile(r.Context(), claims.UserID, update)
		if err != nil {
			writeLookupError(w, r, err)
			return
		}
		response.JSON(w, http.StatusOK, "Profile updated", profileOf(u), nil)
	})))

	// POST /api/v1/auth/change-password: needs the current password, signs every other session out and
	// returns a fresh session for this device.
	mux.Handle("POST /api/v1/auth/change-password", auth.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := auth.ClaimsFrom(r.Context())
		var req struct {
			CurrentPassword string `json:"currentPassword"`
			NewPassword     string `json:"newPassword"`
		}
		if !request.DecodeJSON(w, r, &req) {
			return
		}
		u, err := repo.FindByID(r.Context(), claims.UserID)
		if err != nil {
			writeLookupError(w, r, err)
			return
		}
		fieldErrors := map[string][]string{}
		if req.CurrentPassword == "" {
			fieldErrors["currentPassword"] = []string{"Current password is required"}
		}
		if problems := validatePassword(req.NewPassword, u.Email); len(problems) != 0 {
			fieldErrors["newPassword"] = problems
		} else if req.NewPassword == req.CurrentPassword {
			fieldErrors["newPassword"] = []string{"New password must differ from the current password"}
		}
		if len(fieldErrors) != 0 {
			response.Error(w, r, http.StatusUnprocessableEntity, "VALIDATION_FAILED", "Request validation failed", fieldErrors)
			return
		}
		err = repo.ChangePassword(r.Context(), u.ID, req.CurrentPassword, req.NewPassword)
		switch {
		case dependency.IsUnavailable(err):
			dependency.WriteUnavailable(w, r, err)
			return
		case errors.Is(err, repository.ErrInvalidCredentials):
			response.Error(w, r, http.StatusForbidden, "INVALID_CURRENT_PASSWORD", "The current password is incorrect", nil)
			return
		case err != nil:
			response.Error(w, r, http.StatusInternalServerError, "CHANGE_PASSWORD_FAILED", "Could not change the password", nil)
			return
		}
		if err := tokens.RevokeAllForUser(r.Context(), u.ID); err != nil {
			if dependency.IsUnavailable(err) {
				dependency.WriteUnavailable(w, r, err)
				return
			}
			response.Error(w, r, http.StatusInternalServerError, "CHANGE_PASSWORD_FAILED", "Could not sign out other sessions", nil)
			return
		}
		profile, ok := issueSession(w, r, tokens, u)
		if !ok {
			return
		}
		response.JSON(w, http.StatusOK, "Password changed; other sessions were signed out", profile, nil)
	})))

	if admin != nil {
		registerAdminRoutes(mux, admin)
	}

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
	log.Fatal(http.ListenAndServe(":"+port, NewServerWithAdmin(repo, repository.NewRefreshRepository(repo), repository.NewAdminRepository(repo), true)))
}
