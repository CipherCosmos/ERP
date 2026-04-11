package auth

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var secret []byte
var tokenTTL = 24 * time.Hour

func Configure(jwtSecret string, ttl time.Duration) {
	secret = []byte(jwtSecret)
	if ttl > 0 {
		tokenTTL = ttl
	}
}

func SessionTTL() time.Duration {
	return tokenTTL
}

type Claims struct {
	UserID  string `json:"uid"`
	Role    string `json:"role"`
	StoreID string `json:"sid"`
	jwt.RegisteredClaims
}

func SignToken(userID, role, storeID string, exp time.Duration) (string, error) {
	if len(secret) == 0 {
		return "", fmt.Errorf("jwt not configured")
	}
	if exp <= 0 {
		exp = tokenTTL
	}
	claims := Claims{
		UserID:  userID,
		Role:    role,
		StoreID: storeID,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(exp)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	t := jwt.NewWithClaims(jwt.SigningMethodHS256, &claims)
	return t.SignedString(secret)
}

func ParseToken(tokenString string) (*Claims, error) {
	if len(secret) == 0 {
		return nil, fmt.Errorf("jwt not configured")
	}
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(t *jwt.Token) (interface{}, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return secret, nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}
	return claims, nil
}
