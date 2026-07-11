package handler

import (
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/kefu/unica/admin/internal/audit"
	"github.com/kefu/unica/admin/internal/auth"
	"github.com/kefu/unica/admin/internal/rbac"
)

// AuditLogHandler handles audit log query endpoints.
type AuditLogHandler struct {
	repo *audit.Repository
}

// NewAuditLogHandler creates a new audit log handler.
func NewAuditLogHandler(repo *audit.Repository) *AuditLogHandler {
	return &AuditLogHandler{repo: repo}
}

// HandleAuditLogs handles GET /api/v1/audit-logs with filters and pagination.
// RBAC: SuperAdmin sees all, ProductAdmin sees own product line only.
func (h *AuditLogHandler) HandleAuditLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		ErrorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	claims := auth.GetClaims(r.Context())
	if claims == nil {
		ErrorJSON(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	// Parse query parameters
	q := r.URL.Query()
	filter := audit.QueryFilter{
		ActorID:      q.Get("actor"),
		Action:       q.Get("action"),
		ResourceType: q.Get("resource_type"),
	}

	// Parse pagination
	if limitStr := q.Get("limit"); limitStr != "" {
		if v, err := strconv.Atoi(limitStr); err == nil && v > 0 {
			filter.Limit = v
		}
	}
	if filter.Limit <= 0 {
		filter.Limit = 50
	}

	if offsetStr := q.Get("offset"); offsetStr != "" {
		if v, err := strconv.Atoi(offsetStr); err == nil && v >= 0 {
			filter.Offset = v
		}
	}

	// Parse time range
	if startStr := q.Get("start"); startStr != "" {
		if t, err := time.Parse(time.RFC3339, startStr); err == nil {
			filter.Start = &t
		} else if t, err := time.Parse("2006-01-02", startStr); err == nil {
			filter.Start = &t
		}
	}
	if endStr := q.Get("end"); endStr != "" {
		if t, err := time.Parse(time.RFC3339, endStr); err == nil {
			filter.End = &t
		} else if t, err := time.Parse("2006-01-02", endStr); err == nil {
			endOfDay := t.Add(24*time.Hour - time.Second)
			filter.End = &endOfDay
		}
	}

	// RBAC scoping
	var result *audit.QueryResult
	var err error

	if claims.Role == string(rbac.RoleSuperAdmin) {
		// SuperAdmin: optional product_line_id filter
		if plID := q.Get("product_line_id"); plID != "" {
			filter.ProductLineID = plID
		}
		result, err = h.repo.Query(r.Context(), filter)
	} else {
		// ProductAdmin: restrict to own product lines
		if len(claims.ProductLineIDs) == 0 {
			JSON(w, http.StatusOK, audit.QueryResult{
				Entries: []audit.AuditEntry{},
				Total:   0,
				Limit:   filter.Limit,
				Offset:  filter.Offset,
			})
			return
		}
		result, err = h.repo.QueryByProductLines(r.Context(), filter, claims.ProductLineIDs)
	}

	if err != nil {
		log.Printf("[audit-logs] query error: %v", err)
		ErrorJSON(w, http.StatusInternalServerError, "failed to query audit logs")
		return
	}

	JSON(w, http.StatusOK, result)
}
