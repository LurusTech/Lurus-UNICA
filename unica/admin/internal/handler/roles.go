package handler

import (
	"log"
	"net/http"

	"github.com/kefu/unica/admin/internal/rbac"
	"github.com/kefu/unica/admin/internal/repository"
)

// RoleHandler handles role assignment endpoints.
type RoleHandler struct {
	roleRepo *repository.RoleRepository
	userRepo *repository.UserRepository
}

// NewRoleHandler creates a new role handler.
func NewRoleHandler(roleRepo *repository.RoleRepository, userRepo *repository.UserRepository) *RoleHandler {
	return &RoleHandler{
		roleRepo: roleRepo,
		userRepo: userRepo,
	}
}

type assignRoleRequest struct {
	RoleName      string  `json:"role_name"`
	ProductLineID *string `json:"product_line_id,omitempty"`
}

// HandleAssignRole handles POST /api/v1/users/:id/roles.
func (h *RoleHandler) HandleAssignRole(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		ErrorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	segments := ExtractPathSegments(r.URL.Path, "/api/v1/users/")
	if len(segments) < 2 || segments[1] != "roles" {
		ErrorJSON(w, http.StatusBadRequest, "invalid path")
		return
	}
	userID := segments[0]

	// Verify user exists
	user, err := h.userRepo.GetByID(r.Context(), userID)
	if err != nil {
		log.Printf("[roles] user lookup error: %v", err)
		ErrorJSON(w, http.StatusInternalServerError, "internal error")
		return
	}
	if user == nil {
		ErrorJSON(w, http.StatusNotFound, "user not found")
		return
	}

	var req assignRoleRequest
	if err := DecodeJSON(r, &req); err != nil {
		ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if !rbac.IsValidRole(req.RoleName) {
		ErrorJSON(w, http.StatusBadRequest, "invalid role name")
		return
	}

	// Global roles should not have a product line ID
	if rbac.IsGlobalRole(rbac.Role(req.RoleName)) && req.ProductLineID != nil {
		ErrorJSON(w, http.StatusBadRequest, "global roles cannot be scoped to a product line")
		return
	}

	// Non-global roles require a product line ID
	if !rbac.IsGlobalRole(rbac.Role(req.RoleName)) && req.ProductLineID == nil {
		ErrorJSON(w, http.StatusBadRequest, "product_line_id required for scoped roles")
		return
	}

	// Look up role by name
	role, err := h.roleRepo.GetByName(r.Context(), req.RoleName)
	if err != nil || role == nil {
		log.Printf("[roles] role lookup error: %v", err)
		ErrorJSON(w, http.StatusInternalServerError, "role not found")
		return
	}

	ur, err := h.roleRepo.AssignRole(r.Context(), userID, role.ID, req.ProductLineID)
	if err != nil {
		log.Printf("[roles] assign error: %v", err)
		ErrorJSON(w, http.StatusConflict, "role assignment failed (may already exist)")
		return
	}
	ur.RoleName = role.Name

	JSON(w, http.StatusCreated, ur)
}

// HandleRemoveRole handles DELETE /api/v1/users/:id/roles/:roleId.
func (h *RoleHandler) HandleRemoveRole(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		ErrorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	segments := ExtractPathSegments(r.URL.Path, "/api/v1/users/")
	if len(segments) < 3 || segments[1] != "roles" {
		ErrorJSON(w, http.StatusBadRequest, "invalid path")
		return
	}
	userID := segments[0]
	roleID := segments[2]

	if err := h.roleRepo.RemoveRole(r.Context(), userID, roleID); err != nil {
		log.Printf("[roles] remove error: %v", err)
		ErrorJSON(w, http.StatusNotFound, "role assignment not found")
		return
	}

	JSON(w, http.StatusOK, map[string]string{"message": "role removed"})
}
