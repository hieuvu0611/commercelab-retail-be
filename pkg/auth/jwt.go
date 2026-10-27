package auth

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/hieuvu0211/full-service-shopping/be/pkg/config"
)

type Claims struct {
	UserID         string `json:"userId"`
	Role           string `json:"role"`
	WarehouseScope string `json:"warehouseScope"`
	TokenType      string `json:"tokenType"`
	jwt.RegisteredClaims
}

func SignAccessToken(userID, role, warehouseScope string) (string, error) {
	secret := config.EnvOrDefault("JWT_ACCESS_SECRET", "commerce_lab_access_secret_key_change_in_prod")
	now := time.Now().UTC()
	claims := Claims{
		UserID:         userID,
		Role:           role,
		WarehouseScope: warehouseScope,
		TokenType:      "ACCESS",
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(15 * time.Minute)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
}

func SignRefreshToken(userID, role, warehouseScope string) (string, error) {
	secret := config.EnvOrDefault("JWT_REFRESH_SECRET", "commerce_lab_refresh_secret_key_change_in_prod")
	now := time.Now().UTC()
	claims := Claims{
		UserID:         userID,
		Role:           role,
		WarehouseScope: warehouseScope,
		TokenType:      "REFRESH",
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(7 * 24 * time.Hour)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
}

func ParseAccessToken(tokenStr string) (*Claims, error) {
	secret := config.EnvOrDefault("JWT_ACCESS_SECRET", "commerce_lab_access_secret_key_change_in_prod")
	return parseWithSecret(tokenStr, secret, "ACCESS")
}

func ParseRefreshToken(tokenStr string) (*Claims, error) {
	secret := config.EnvOrDefault("JWT_REFRESH_SECRET", "commerce_lab_refresh_secret_key_change_in_prod")
	return parseWithSecret(tokenStr, secret, "REFRESH")
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
