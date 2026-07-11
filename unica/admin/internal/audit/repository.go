package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// AuditEntry represents a single audit log record.
type AuditEntry struct {
	ID            int64            `json:"id"`
	ActorID       string           `json:"actor_id"`
	ActorRole     string           `json:"actor_role"`
	Action        string           `json:"action"`
	ResourceType  string           `json:"resource_type"`
	ResourceID    string           `json:"resource_id"`
	ProductLineID *string          `json:"product_line_id,omitempty"`
	BeforeState   *json.RawMessage `json:"before_state,omitempty"`
	AfterState    *json.RawMessage `json:"after_state,omitempty"`
	IPAddress     *string          `json:"ip_address,omitempty"`
	CreatedAt     time.Time        `json:"created_at"`
}

// QueryFilter holds filter parameters for querying audit logs.
type QueryFilter struct {
	ActorID       string
	Action        string
	ResourceType  string
	ProductLineID string // used for RBAC scoping
	Start         *time.Time
	End           *time.Time
	Limit         int
	Offset        int
}

// QueryResult holds paginated audit log results.
type QueryResult struct {
	Entries []AuditEntry `json:"entries"`
	Total   int          `json:"total"`
	Limit   int          `json:"limit"`
	Offset  int          `json:"offset"`
}

// Repository provides database operations for audit logs.
type Repository struct {
	db *sql.DB
}

// NewRepository creates a new audit log repository.
func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

// Insert writes a single audit entry to the database.
func (r *Repository) Insert(ctx context.Context, entry *AuditEntry) error {
	query := `INSERT INTO audit_logs (actor_id, actor_role, action, resource_type, resource_id, product_line_id, before_state, after_state, ip_address, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`

	_, err := r.db.ExecContext(ctx, query,
		entry.ActorID,
		entry.ActorRole,
		entry.Action,
		entry.ResourceType,
		entry.ResourceID,
		entry.ProductLineID,
		entry.BeforeState,
		entry.AfterState,
		entry.IPAddress,
		entry.CreatedAt,
	)
	return err
}

// Query retrieves audit log entries matching the given filters with pagination.
func (r *Repository) Query(ctx context.Context, filter QueryFilter) (*QueryResult, error) {
	if filter.Limit <= 0 {
		filter.Limit = 50
	}
	if filter.Limit > 200 {
		filter.Limit = 200
	}

	// Build WHERE clauses
	var conditions []string
	var args []interface{}
	argIdx := 1

	if filter.ActorID != "" {
		conditions = append(conditions, fmt.Sprintf("actor_id = $%d", argIdx))
		args = append(args, filter.ActorID)
		argIdx++
	}
	if filter.Action != "" {
		conditions = append(conditions, fmt.Sprintf("action = $%d", argIdx))
		args = append(args, filter.Action)
		argIdx++
	}
	if filter.ResourceType != "" {
		conditions = append(conditions, fmt.Sprintf("resource_type = $%d", argIdx))
		args = append(args, filter.ResourceType)
		argIdx++
	}
	if filter.ProductLineID != "" {
		conditions = append(conditions, fmt.Sprintf("product_line_id = $%d", argIdx))
		args = append(args, filter.ProductLineID)
		argIdx++
	}
	if filter.Start != nil {
		conditions = append(conditions, fmt.Sprintf("created_at >= $%d", argIdx))
		args = append(args, *filter.Start)
		argIdx++
	}
	if filter.End != nil {
		conditions = append(conditions, fmt.Sprintf("created_at <= $%d", argIdx))
		args = append(args, *filter.End)
		argIdx++
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	// Count total matches
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM audit_logs %s", whereClause)
	var total int
	if err := r.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("count audit logs: %w", err)
	}

	// Fetch page
	dataQuery := fmt.Sprintf(
		`SELECT id, actor_id, actor_role, action, resource_type, resource_id,
		        product_line_id, before_state, after_state, ip_address, created_at
		 FROM audit_logs %s
		 ORDER BY created_at DESC
		 LIMIT $%d OFFSET $%d`,
		whereClause, argIdx, argIdx+1,
	)
	args = append(args, filter.Limit, filter.Offset)

	rows, err := r.db.QueryContext(ctx, dataQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("query audit logs: %w", err)
	}
	defer rows.Close()

	var entries []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var beforeState, afterState []byte
		if err := rows.Scan(
			&e.ID, &e.ActorID, &e.ActorRole, &e.Action,
			&e.ResourceType, &e.ResourceID, &e.ProductLineID,
			&beforeState, &afterState, &e.IPAddress, &e.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan audit log row: %w", err)
		}
		if beforeState != nil {
			raw := json.RawMessage(beforeState)
			e.BeforeState = &raw
		}
		if afterState != nil {
			raw := json.RawMessage(afterState)
			e.AfterState = &raw
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate audit log rows: %w", err)
	}

	if entries == nil {
		entries = []AuditEntry{}
	}

	return &QueryResult{
		Entries: entries,
		Total:   total,
		Limit:   filter.Limit,
		Offset:  filter.Offset,
	}, nil
}

// QueryByProductLines retrieves entries filtered to a set of product line IDs (for RBAC).
func (r *Repository) QueryByProductLines(ctx context.Context, filter QueryFilter, productLineIDs []string) (*QueryResult, error) {
	if len(productLineIDs) == 0 {
		return &QueryResult{Entries: []AuditEntry{}, Total: 0, Limit: filter.Limit, Offset: filter.Offset}, nil
	}

	// If a specific product_line_id filter is set, verify it's in the allowed set
	if filter.ProductLineID != "" {
		allowed := false
		for _, id := range productLineIDs {
			if id == filter.ProductLineID {
				allowed = true
				break
			}
		}
		if !allowed {
			return &QueryResult{Entries: []AuditEntry{}, Total: 0, Limit: filter.Limit, Offset: filter.Offset}, nil
		}
		// Use the standard query since we've verified access
		return r.Query(ctx, filter)
	}

	// For unfiltered queries, restrict to allowed product lines
	// Build a query that includes only entries matching the allowed product line IDs
	if filter.Limit <= 0 {
		filter.Limit = 50
	}
	if filter.Limit > 200 {
		filter.Limit = 200
	}

	var conditions []string
	var args []interface{}
	argIdx := 1

	// Product line scope
	placeholders := make([]string, len(productLineIDs))
	for i, plID := range productLineIDs {
		placeholders[i] = fmt.Sprintf("$%d", argIdx)
		args = append(args, plID)
		argIdx++
	}
	conditions = append(conditions, fmt.Sprintf("product_line_id IN (%s)", strings.Join(placeholders, ",")))

	if filter.ActorID != "" {
		conditions = append(conditions, fmt.Sprintf("actor_id = $%d", argIdx))
		args = append(args, filter.ActorID)
		argIdx++
	}
	if filter.Action != "" {
		conditions = append(conditions, fmt.Sprintf("action = $%d", argIdx))
		args = append(args, filter.Action)
		argIdx++
	}
	if filter.ResourceType != "" {
		conditions = append(conditions, fmt.Sprintf("resource_type = $%d", argIdx))
		args = append(args, filter.ResourceType)
		argIdx++
	}
	if filter.Start != nil {
		conditions = append(conditions, fmt.Sprintf("created_at >= $%d", argIdx))
		args = append(args, *filter.Start)
		argIdx++
	}
	if filter.End != nil {
		conditions = append(conditions, fmt.Sprintf("created_at <= $%d", argIdx))
		args = append(args, *filter.End)
		argIdx++
	}

	whereClause := "WHERE " + strings.Join(conditions, " AND ")

	// Count
	var total int
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM audit_logs %s", whereClause)
	if err := r.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("count audit logs: %w", err)
	}

	// Fetch
	dataQuery := fmt.Sprintf(
		`SELECT id, actor_id, actor_role, action, resource_type, resource_id,
		        product_line_id, before_state, after_state, ip_address, created_at
		 FROM audit_logs %s
		 ORDER BY created_at DESC
		 LIMIT $%d OFFSET $%d`,
		whereClause, argIdx, argIdx+1,
	)
	args = append(args, filter.Limit, filter.Offset)

	rows, err := r.db.QueryContext(ctx, dataQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("query audit logs: %w", err)
	}
	defer rows.Close()

	var entries []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var beforeState, afterState []byte
		if err := rows.Scan(
			&e.ID, &e.ActorID, &e.ActorRole, &e.Action,
			&e.ResourceType, &e.ResourceID, &e.ProductLineID,
			&beforeState, &afterState, &e.IPAddress, &e.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan audit log row: %w", err)
		}
		if beforeState != nil {
			raw := json.RawMessage(beforeState)
			e.BeforeState = &raw
		}
		if afterState != nil {
			raw := json.RawMessage(afterState)
			e.AfterState = &raw
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate audit log rows: %w", err)
	}

	if entries == nil {
		entries = []AuditEntry{}
	}

	return &QueryResult{
		Entries: entries,
		Total:   total,
		Limit:   filter.Limit,
		Offset:  filter.Offset,
	}, nil
}
