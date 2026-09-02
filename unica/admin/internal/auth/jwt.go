package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Claims represents the JWT claims for an authenticated user.
//
// One role and one tenant, both scalars: an admin belongs to no tenant and
// TenantID is empty, a user belongs to exactly one and TenantID names it.
// There is no list here because there is nothing a list could hold — a second
// tenant for the same person is a second account.
type Claims struct {
	UserID   string `json:"user_id"`
	Email    string `json:"email"`
	Role     string `json:"role"`
	TenantID string `json:"tenant_id"`
	jwt.RegisteredClaims
}

// TokenPair holds both access and refresh tokens.
type TokenPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"`
}

// JWTManager handles token generation and validation.
type JWTManager struct {
	secret          []byte
	accessTokenTTL  time.Duration
	refreshTokenTTL time.Duration
}

// NewJWTManager creates a new JWT manager with the given signing secret and TTLs.
func NewJWTManager(secret string, accessTTL, refreshTTL time.Duration) *JWTManager {
	return &JWTManager{
		secret:          []byte(secret),
		accessTokenTTL:  accessTTL,
		refreshTokenTTL: refreshTTL,
	}
}

// newTokenID returns a random 128-bit identifier, hex encoded, for use as a
// token's jti. A failing random source is reported, never papered over: an
// empty or predictable jti would reintroduce the collision this guards against.
func newTokenID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("failed to generate token id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// GenerateTokenPair creates a new access + refresh token pair for a user.
// tenantID is empty for an admin, which belongs to no tenant.
//
// Each token carries its own random jti, so two pairs issued within the same
// second are distinct strings and one cannot be mistaken for the other.
func (m *JWTManager) GenerateTokenPair(userID, email, role, tenantID string) (*TokenPair, error) {
	now := time.Now()
	accessExp := now.Add(m.accessTokenTTL)

	accessID, err := newTokenID()
	if err != nil {
		return nil, err
	}

	accessClaims := &Claims{
		UserID:   userID,
		Email:    email,
		Role:     role,
		TenantID: tenantID,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        accessID,
			ExpiresAt: jwt.NewNumericDate(accessExp),
			IssuedAt:  jwt.NewNumericDate(now),
			Issuer:    "unica-admin",
			Subject:   userID,
		},
	}

	accessToken := jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims)
	accessStr, err := accessToken.SignedString(m.secret)
	if err != nil {
		return nil, fmt.Errorf("failed to sign access token: %w", err)
	}

	refreshExp := now.Add(m.refreshTokenTTL)

	refreshID, err := newTokenID()
	if err != nil {
		return nil, err
	}

	refreshClaims := &Claims{
		UserID: userID,
		Email:  email,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        refreshID,
			ExpiresAt: jwt.NewNumericDate(refreshExp),
			IssuedAt:  jwt.NewNumericDate(now),
			Issuer:    "unica-admin",
			Subject:   userID,
		},
	}

	refreshToken := jwt.NewWithClaims(jwt.SigningMethodHS256, refreshClaims)
	refreshStr, err := refreshToken.SignedString(m.secret)
	if err != nil {
		return nil, fmt.Errorf("failed to sign refresh token: %w", err)
	}

	return &TokenPair{
		AccessToken:  accessStr,
		RefreshToken: refreshStr,
		ExpiresAt:    accessExp.Unix(),
	}, nil
}

// ValidateToken parses and validates a JWT token string, returning its claims.
func (m *JWTManager) ValidateToken(tokenStr string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return m.secret, nil
	})
	if err != nil {
		return nil, fmt.Errorf("invalid token: %w", err)
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token claims")
	}

	return claims, nil
}

// HashToken creates a SHA-256 hash of a token for safe storage.
func HashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}
