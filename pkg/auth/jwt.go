package auth

import (
	"errors"
	"sync/atomic"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/config"
)

// MinSecretBytes is the minimum length of a JWT signing secret.
const MinSecretBytes = 32

// Environment variables that hold the signing secrets.
const (
	AccessSecretEnvVar  = "JWT_ACCESS_SECRET"
	RefreshSecretEnvVar = "JWT_REFRESH_SECRET"
)

// ErrNotConfigured is returned when tokens are used before Configure was called.
var ErrNotConfigured = errors.New("auth: JWT secrets are not configured")

// Secrets are the keys used to sign and verify tokens.
type Secrets struct {
	Access  string
	Refresh string
}

var configured atomic.Pointer[Secrets]

// Configure installs the signing secrets. Services call it once at start-up (see LoadSecrets).
func Configure(s Secrets) error {
	if s.Access == "" || s.Refresh == "" {
		return ErrNotConfigured
	}
	cp := s
	configured.Store(&cp)
	return nil
}

// LoadSecrets reads the secrets from the environment into l (they are mandatory, at least MinSecretBytes,
// except in APP_ENV=dev) and configures the package when they are valid.
func LoadSecrets(l *config.Loader) Secrets {
	s := Secrets{
		Access:  l.Secret(AccessSecretEnvVar, MinSecretBytes),
		Refresh: l.Secret(RefreshSecretEnvVar, MinSecretBytes),
	}
	if l.Err() == nil {
		_ = Configure(s)
	}
	return s
}

// Reset removes the configured secrets. It exists for tests.
func Reset() {
	configured.Store(nil)
}

func currentSecrets() (Secrets, error) {
	s := configured.Load()
	if s == nil {
		return Secrets{}, ErrNotConfigured
	}
	return *s, nil
}

// Claims are the token claims. Role and WarehouseScope are the legacy single-role fields kept during the
// transition to Roles and WarehouseScopes (P1-T05); new code must use the plural fields.
type Claims struct {
	UserID          string   `json:"userId"`
	Role            string   `json:"role"`
	WarehouseScope  string   `json:"warehouseScope"`
	Roles           []string `json:"roles,omitempty"`
	WarehouseScopes []string `json:"warehouseScopes,omitempty"`
	TokenType       string   `json:"tokenType"`
	jwt.RegisteredClaims
}

// Identity is what an access token states about its subject.
type Identity struct {
	UserID          string
	Role            string // legacy single role
	WarehouseScope  string // legacy single scope
	Roles           []string
	WarehouseScopes []string // warehouse ids; "ALL" means every warehouse
}

// HasRole reports whether the claims carry role.
func (c *Claims) HasRole(role string) bool {
	for _, r := range c.Roles {
		if r == role {
			return true
		}
	}
	return false
}

func SignAccessToken(id Identity) (string, error) {
	secrets, err := currentSecrets()
	if err != nil {
		return "", err
	}
	secret := secrets.Access
	now := time.Now().UTC()
	claims := Claims{
		UserID:          id.UserID,
		Role:            id.Role,
		WarehouseScope:  id.WarehouseScope,
		Roles:           id.Roles,
		WarehouseScopes: id.WarehouseScopes,
		TokenType:       "ACCESS",
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   id.UserID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(15 * time.Minute)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
}

// SignRefreshToken signs a refresh token whose "jti" is tokenID, so the server can find and rotate it.
func SignRefreshToken(userID, role, warehouseScope, tokenID string) (string, error) {
	secrets, err := currentSecrets()
	if err != nil {
		return "", err
	}
	secret := secrets.Refresh
	now := time.Now().UTC()
	claims := Claims{
		UserID:         userID,
		Role:           role,
		WarehouseScope: warehouseScope,
		TokenType:      "REFRESH",
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        tokenID,
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(7 * 24 * time.Hour)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
}

func ParseAccessToken(tokenStr string) (*Claims, error) {
	secrets, err := currentSecrets()
	if err != nil {
		return nil, err
	}
	return parseWithSecret(tokenStr, secrets.Access, "ACCESS")
}

func ParseRefreshToken(tokenStr string) (*Claims, error) {
	secrets, err := currentSecrets()
	if err != nil {
		return nil, err
	}
	return parseWithSecret(tokenStr, secrets.Refresh, "REFRESH")
}

func parseWithSecret(tokenStr, secret, expectedType string) (*Claims, error) {
	tok, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("thuật toán ký không hợp lệ")
		}
		return []byte(secret), nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := tok.Claims.(*Claims)
	if !ok || !tok.Valid || claims.TokenType != expectedType {
		return nil, errors.New("token không hợp lệ")
	}
	return claims, nil
}
