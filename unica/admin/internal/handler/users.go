package handler

import (
	"log"
	"net/http"

	"github.com/kefu/unica/admin/internal/auth"
	"github.com/kefu/unica/admin/internal/repository"
)

// UserHandler handles user CRUD endpoints.
type UserHandler struct {
	userRepo *repository.UserRepository
	roleRepo *repository.RoleRepository
	bcryptCost int
}

// NewUserHandler creates a new user handler.
func NewUserHandler(userRepo *repository.UserRepository, roleRepo *repository.RoleRepository, bcryptCost int) *UserHandler {
	return &UserHandler{
		userRepo:   userRepo,
		roleRepo:   roleRepo,
		bcryptCost: bcryptCost,
	}
}

type createUserRequest struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
}

type updateUserRequest struct {
	DisplayName string `json:"display_name"`
	IsActive    *bool  `json:"is_active"`
}

// userResponse represents a safe user response (no password hash).
type userResponse struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	IsActive    bool   `json:"is_active"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

func toUserResponse(u *repository.User) *userResponse {
	return &userResponse{
		ID:          u.ID,
		Email:       u.Email,
		DisplayName: u.DisplayName,
		IsActive:    u.IsActive,
		CreatedAt:   u.CreatedAt.Format("2006-01-02T15:04:05Z"),
		UpdatedAt:   u.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	}
}

// HandleUsers handles GET (list) and POST (create) on /api/v1/users.
func (h *UserHandler) HandleUsers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.listUsers(w, r)
	case http.MethodPost:
		h.createUser(w, r)
	default:
		ErrorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// HandleUser handles GET/PUT/DELETE on /api/v1/users/:id.
func (h *UserHandler) HandleUser(w http.ResponseWriter, r *http.Request) {
	id := ExtractPathParam(r.URL.Path, "/api/v1/users/")
	if id == "" {
		ErrorJSON(w, http.StatusBadRequest, "user id required")
		return
	}

	// Check if this is a roles sub-resource
	segments := ExtractPathSegments(r.URL.Path, "/api/v1/users/")
	if len(segments) >= 2 && segments[1] == "roles" {
		// Delegate to role handler - should not reach here (routed separately)
		ErrorJSON(w, http.StatusNotFound, "not found")
		return
	}

	switch r.Method {
	case http.MethodGet:
		h.getUser(w, r, id)
	case http.MethodPut:
		h.updateUser(w, r, id)
	case http.MethodDelete:
		h.deleteUser(w, r, id)
	default:
		ErrorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *UserHandler) listUsers(w http.ResponseWriter, r *http.Request) {
	claims := auth.GetClaims(r.Context())

	// Filter by product line scope for non-SuperAdmin users
	var plIDs []string
	if claims != nil && claims.Role != "super_admin" {
		plIDs = claims.ProductLineIDs
	}

	users, err := h.userRepo.List(r.Context(), plIDs)
	if err != nil {
		log.Printf("[users] list error: %v", err)
		ErrorJSON(w, http.StatusInternalServerError, "failed to list users")
		return
	}

	resp := make([]userResponse, len(users))
	for i, u := range users {
		resp[i] = *toUserResponse(&u)
	}
	JSON(w, http.StatusOK, resp)
}

func (h *UserHandler) createUser(w http.ResponseWriter, r *http.Request) {
	var req createUserRequest
	if err := DecodeJSON(r, &req); err != nil {
		ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Email == "" || req.Password == "" || req.DisplayName == "" {
		ErrorJSON(w, http.StatusBadRequest, "email, password, and display_name required")
		return
	}

	// Check for existing user
	existing, err := h.userRepo.GetByEmail(r.Context(), req.Email)
	if err != nil {
		log.Printf("[users] email check error: %v", err)
		ErrorJSON(w, http.StatusInternalServerError, "internal error")
		return
	}
	if existing != nil {
		ErrorJSON(w, http.StatusConflict, "email already in use")
		return
	}

	hash, err := auth.HashPassword(req.Password, h.bcryptCost)
	if err != nil {
		log.Printf("[users] password hash error: %v", err)
		ErrorJSON(w, http.StatusInternalServerError, "failed to hash password")
		return
	}

	user, err := h.userRepo.Create(r.Context(), req.Email, hash, req.DisplayName)
	if err != nil {
		log.Printf("[users] create error: %v", err)
		ErrorJSON(w, http.StatusInternalServerError, "failed to create user")
		return
	}

	JSON(w, http.StatusCreated, toUserResponse(user))
}

func (h *UserHandler) getUser(w http.ResponseWriter, r *http.Request, id string) {
	user, err := h.userRepo.GetByID(r.Context(), id)
	if err != nil {
		log.Printf("[users] get error: %v", err)
		ErrorJSON(w, http.StatusInternalServerError, "internal error")
		return
	}
	if user == nil {
		ErrorJSON(w, http.StatusNotFound, "user not found")
		return
	}

	// Get user roles
	roles, err := h.roleRepo.GetUserRoles(r.Context(), id)
	if err != nil {
		log.Printf("[users] get roles error: %v", err)
		ErrorJSON(w, http.StatusInternalServerError, "internal error")
		return
	}

	JSON(w, http.StatusOK, map[string]interface{}{
		"user":  toUserResponse(user),
		"roles": roles,
	})
}

func (h *UserHandler) updateUser(w http.ResponseWriter, r *http.Request, id string) {
	var req updateUserRequest
	if err := DecodeJSON(r, &req); err != nil {
		ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}

	existing, err := h.userRepo.GetByID(r.Context(), id)
	if err != nil {
		log.Printf("[users] get error: %v", err)
		ErrorJSON(w, http.StatusInternalServerError, "internal error")
		return
	}
	if existing == nil {
		ErrorJSON(w, http.StatusNotFound, "user not found")
		return
	}

	displayName := existing.DisplayName
	if req.DisplayName != "" {
		displayName = req.DisplayName
	}
	isActive := existing.IsActive
	if req.IsActive != nil {
		isActive = *req.IsActive
	}

	user, err := h.userRepo.Update(r.Context(), id, displayName, isActive)
	if err != nil {
		log.Printf("[users] update error: %v", err)
		ErrorJSON(w, http.StatusInternalServerError, "failed to update user")
		return
	}

	JSON(w, http.StatusOK, toUserResponse(user))
}

func (h *UserHandler) deleteUser(w http.ResponseWriter, r *http.Request, id string) {
	if err := h.userRepo.Deactivate(r.Context(), id); err != nil {
		log.Printf("[users] deactivate error: %v", err)
		ErrorJSON(w, http.StatusInternalServerError, "failed to deactivate user")
		return
	}
	JSON(w, http.StatusOK, map[string]string{"message": "user deactivated"})
}
