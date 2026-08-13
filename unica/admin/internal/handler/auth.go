package handler

import (
	"log"
	"net/http"
	"time"

	"github.com/kefu/unica/admin/internal/auth"
	"github.com/kefu/unica/admin/internal/repository"
	"github.com/redis/go-redis/v9"
)

// AuthHandler handles authentication endpoints.
type AuthHandler struct {
	userRepo   *repository.UserRepository
	jwtMgr     *auth.JWTManager
	rdb        *redis.Client
	refreshTTL time.Duration
}

// NewAuthHandler creates a new auth handler.
func NewAuthHandler(userRepo *repository.UserRepository, jwtMgr *auth.JWTManager, rdb *redis.Client, refreshTTL time.Duration) *AuthHandler {
	return &AuthHandler{
		userRepo:   userRepo,
		jwtMgr:     jwtMgr,
		rdb:        rdb,
		refreshTTL: refreshTTL,
	}
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

// HandleLogin processes login requests.
func (h *AuthHandler) HandleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		ErrorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req loginRequest
	if err := DecodeJSON(r, &req); err != nil {
		ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Email == "" || req.Password == "" {
		ErrorJSON(w, http.StatusBadRequest, "email and password required")
		return
	}

	// Look up user by email
	user, err := h.userRepo.GetByEmail(r.Context(), req.Email)
	if err != nil {
		log.Printf("[auth] login lookup error: %v", err)
		ErrorJSON(w, http.StatusInternalServerError, "internal error")
		return
	}
	if user == nil {
		ErrorJSON(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	if !user.IsActive {
		ErrorJSON(w, http.StatusUnauthorized, "account is deactivated")
		return
	}

	// Verify password
	if err := auth.VerifyPassword(user.PasswordHash, req.Password); err != nil {
		ErrorJSON(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	tokenPair, err := h.generateTokensForUser(user)
	if err != nil {
		log.Printf("[auth] token generation error: %v", err)
		ErrorJSON(w, http.StatusInternalServerError, "failed to generate tokens")
		return
	}

	// Store refresh token hash in Redis for revocation support
	tokenHash := auth.HashToken(tokenPair.RefreshToken)
	if err := h.rdb.Set(r.Context(), "refresh:"+tokenHash, user.ID, h.refreshTTL).Err(); err != nil {
		log.Printf("[auth] failed to store refresh token: %v", err)
	}

	JSON(w, http.StatusOK, tokenPair)
}

// HandleRefresh processes token refresh requests.
func (h *AuthHandler) HandleRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		ErrorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req refreshRequest
	if err := DecodeJSON(r, &req); err != nil {
		ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.RefreshToken == "" {
		ErrorJSON(w, http.StatusBadRequest, "refresh_token required")
		return
	}

	// Validate the refresh token
	claims, err := h.jwtMgr.ValidateToken(req.RefreshToken)
	if err != nil {
		ErrorJSON(w, http.StatusUnauthorized, "invalid refresh token")
		return
	}

	// Check if token is revoked in Redis
	tokenHash := auth.HashToken(req.RefreshToken)
	exists, err := h.rdb.Exists(r.Context(), "refresh:"+tokenHash).Result()
	if err != nil || exists == 0 {
		ErrorJSON(w, http.StatusUnauthorized, "refresh token revoked or expired")
		return
	}

	// Delete old refresh token
	h.rdb.Del(r.Context(), "refresh:"+tokenHash)

	// Get user and generate new tokens
	user, err := h.userRepo.GetByID(r.Context(), claims.UserID)
	if err != nil || user == nil {
		ErrorJSON(w, http.StatusUnauthorized, "user not found")
		return
	}

	if !user.IsActive {
		ErrorJSON(w, http.StatusUnauthorized, "account is deactivated")
		return
	}

	tokenPair, err := h.generateTokensForUser(user)
	if err != nil {
		log.Printf("[auth] token refresh error: %v", err)
		ErrorJSON(w, http.StatusInternalServerError, "failed to generate tokens")
		return
	}

	// Store new refresh token
	newHash := auth.HashToken(tokenPair.RefreshToken)
	if err := h.rdb.Set(r.Context(), "refresh:"+newHash, user.ID, h.refreshTTL).Err(); err != nil {
		log.Printf("[auth] failed to store refresh token: %v", err)
	}

	JSON(w, http.StatusOK, tokenPair)
}

// generateTokensForUser issues a token pair from the account row itself: role
// and tenant are columns on users, so signing in needs no second query.
func (h *AuthHandler) generateTokensForUser(user *repository.User) (*auth.TokenPair, error) {
	tenantID := ""
	if user.ProductLineID != nil {
		tenantID = *user.ProductLineID
	}
	return h.jwtMgr.GenerateTokenPair(user.ID, user.Email, user.Role, tenantID)
}
