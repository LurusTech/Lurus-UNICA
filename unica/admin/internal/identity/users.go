package identity

import (
	"log"
	"net/http"
	"strings"

	"github.com/kefu/unica/admin/internal/auth"
	"github.com/kefu/unica/admin/internal/rbac"
	"github.com/kefu/unica/admin/internal/repository"
)

// UserHandler handles user CRUD endpoints. The whole subtree is administrator
// territory: accounts are a platform concern, and a tenant's own people are
// created by whoever runs the platform.
type UserHandler struct {
	userRepo   *repository.UserRepository
	bcryptCost int
}

// NewUserHandler creates a new user handler.
func NewUserHandler(userRepo *repository.UserRepository, bcryptCost int) *UserHandler {
	return &UserHandler{
		userRepo:   userRepo,
		bcryptCost: bcryptCost,
	}
}

// createUserRequest carries the account and, in the same body, what it may
// touch. Role and tenant are not a follow-up grant: an account that exists
// without them could not be authorised at all.
type createUserRequest struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	TenantID    string `json:"tenant_id"`
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
	Role        string `json:"role"`
	TenantID    string `json:"tenant_id,omitempty"`
	IsActive    bool   `json:"is_active"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

func toUserResponse(u *repository.User) *userResponse {
	resp := &userResponse{
		ID:          u.ID,
		Email:       u.Email,
		DisplayName: u.DisplayName,
		Role:        u.Role,
		IsActive:    u.IsActive,
		CreatedAt:   u.CreatedAt.Format("2006-01-02T15:04:05Z"),
		UpdatedAt:   u.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	}
	if u.ProductLineID != nil {
		resp.TenantID = *u.ProductLineID
	}
	return resp
}

// HandleUsers handles GET (list) and POST (create) on /api/v1/users.
func (h *UserHandler) HandleUsers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.listUsers(w, r)
	case http.MethodPost:
		h.createUser(w, r)
	default:
		errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// HandleUser handles GET/PUT/DELETE on /api/v1/users/:id.
func (h *UserHandler) HandleUser(w http.ResponseWriter, r *http.Request) {
	segments := pathSegments(r.URL.Path, "/api/v1/users/")
	if len(segments) != 1 {
		errorJSON(w, http.StatusNotFound, "not found")
		return
	}
	id := segments[0]

	switch r.Method {
	case http.MethodGet:
		h.getUser(w, r, id)
	case http.MethodPut:
		h.updateUser(w, r, id)
	case http.MethodDelete:
		h.deleteUser(w, r, id)
	default:
		errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *UserHandler) listUsers(w http.ResponseWriter, r *http.Request) {
	// The route is administrator-only, so the roster is unfiltered unless the
	// caller asks for one tenant's people.
	tenantID := strings.TrimSpace(r.URL.Query().Get("tenant_id"))

	users, err := h.userRepo.List(r.Context(), tenantID)
	if err != nil {
		log.Printf("[users] list error: %v", err)
		errorJSON(w, http.StatusInternalServerError, "failed to list users")
		return
	}

	resp := make([]userResponse, len(users))
	for i, u := range users {
		resp[i] = *toUserResponse(&u)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *UserHandler) createUser(w http.ResponseWriter, r *http.Request) {
	var req createUserRequest
	if err := decodeJSON(r, &req); err != nil {
		errorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}

	req.Email = strings.TrimSpace(req.Email)
	req.DisplayName = strings.TrimSpace(req.DisplayName)
	req.Role = strings.TrimSpace(req.Role)
	req.TenantID = strings.TrimSpace(req.TenantID)

	if req.Email == "" || req.Password == "" || req.DisplayName == "" {
		errorJSON(w, http.StatusBadRequest, "email, password, and display_name required")
		return
	}

	tenantID, errMsg := validateRoleAndTenant(req.Role, req.TenantID)
	if errMsg != "" {
		errorJSON(w, http.StatusBadRequest, errMsg)
		return
	}

	// Check for existing user
	existing, err := h.userRepo.GetByEmail(r.Context(), req.Email)
	if err != nil {
		log.Printf("[users] email check error: %v", err)
		errorJSON(w, http.StatusInternalServerError, "internal error")
		return
	}
	if existing != nil {
		errorJSON(w, http.StatusConflict, "email already in use")
		return
	}

	hash, err := auth.HashPassword(req.Password, h.bcryptCost)
	if err != nil {
		log.Printf("[users] password hash error: %v", err)
		errorJSON(w, http.StatusInternalServerError, "failed to hash password")
		return
	}

	user, err := h.userRepo.Create(r.Context(), req.Email, hash, req.DisplayName, req.Role, tenantID)
	if err != nil {
		log.Printf("[users] create error: %v", err)
		errorJSON(w, http.StatusInternalServerError, "failed to create user")
		return
	}

	writeJSON(w, http.StatusCreated, toUserResponse(user))
}

// validateRoleAndTenant enforces the pairing the users table also constrains:
// an admin belongs to no tenant, a user belongs to exactly one. Rejecting it
// here turns a constraint violation into a message that names the field.
func validateRoleAndTenant(role, tenantID string) (*string, string) {
	switch role {
	case rbac.RoleAdmin:
		if tenantID != "" {
			return nil, "an admin belongs to no tenant; omit tenant_id"
		}
		return nil, ""
	case rbac.RoleUser:
		if tenantID == "" {
			return nil, "tenant_id is required for role \"user\""
		}
		id := tenantID
		return &id, ""
	default:
		return nil, "role must be \"" + rbac.RoleAdmin + "\" or \"" + rbac.RoleUser + "\""
	}
}

func (h *UserHandler) getUser(w http.ResponseWriter, r *http.Request, id string) {
	user, err := h.userRepo.GetByID(r.Context(), id)
	if err != nil {
		log.Printf("[users] get error: %v", err)
		errorJSON(w, http.StatusInternalServerError, "internal error")
		return
	}
	if user == nil {
		errorJSON(w, http.StatusNotFound, "user not found")
		return
	}

	writeJSON(w, http.StatusOK, toUserResponse(user))
}

func (h *UserHandler) updateUser(w http.ResponseWriter, r *http.Request, id string) {
	var req updateUserRequest
	if err := decodeJSON(r, &req); err != nil {
		errorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}

	existing, err := h.userRepo.GetByID(r.Context(), id)
	if err != nil {
		log.Printf("[users] get error: %v", err)
		errorJSON(w, http.StatusInternalServerError, "internal error")
		return
	}
	if existing == nil {
		errorJSON(w, http.StatusNotFound, "user not found")
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
		errorJSON(w, http.StatusInternalServerError, "failed to update user")
		return
	}

	writeJSON(w, http.StatusOK, toUserResponse(user))
}

func (h *UserHandler) deleteUser(w http.ResponseWriter, r *http.Request, id string) {
	if err := h.userRepo.Deactivate(r.Context(), id); err != nil {
		log.Printf("[users] deactivate error: %v", err)
		errorJSON(w, http.StatusInternalServerError, "failed to deactivate user")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "user deactivated"})
}
