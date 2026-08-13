package repository

import (
	"context"
	"fmt"
)

// TenantDataDeletion counts the rows a tenant removal deleted, one field per
// table, in the order they were deleted. It is reported back to the caller
// because a removal that silently found nothing and one that erased a live
// tenant otherwise look the same.
type TenantDataDeletion struct {
	Messages         int64 `json:"messages"`
	Conversations    int64 `json:"conversations"`
	Customers        int64 `json:"customers"`
	ChannelConfigs   int64 `json:"channel_configs"`
	Channels         int64 `json:"channels"`
	KnowledgeDocs    int64 `json:"knowledge_docs"`
	OntologyVersions int64 `json:"ontology_versions"`
	ClaimViolations  int64 `json:"claim_violations"`
	Users            int64 `json:"users"`
}

// deleteCustomersQuery removes the end customers a tenant accumulated.
//
// They carry no product line of their own: a customer is reached through the
// conversations that name it, or through the channel it first wrote in — and
// channels live in two tables, the legacy one and the one the admin API writes.
// The final clause keeps a customer that some other tenant's conversation still
// references, which the unique key on (platform_identity, channel_id) makes
// unlikely but does not forbid.
const deleteCustomersQuery = `
	DELETE FROM customers
	WHERE (id = ANY($2)
	       OR channel_id IN (SELECT id FROM channels WHERE product_line_id = $1)
	       OR channel_id IN (SELECT id FROM channel_configs WHERE product_line_id = $1))
	  AND NOT EXISTS (SELECT 1 FROM conversations c WHERE c.customer_id = customers.id)`

// DeleteTenantData removes every row a tenant owns except the product_lines row
// itself, in one transaction.
//
// The order is the foreign keys read backwards: messages hang off conversations,
// conversations off customers, and the tenant's accounts reference the product
// line the caller deletes afterwards. All of it commits or none of it does,
// because a half-erased tenant leaves conversations pointing at customers that
// no longer exist.
func (r *ProductLineRepository) DeleteTenantData(ctx context.Context, id string) (TenantDataDeletion, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return TenantDataDeletion{}, fmt.Errorf("begin tenant deletion: %w", err)
	}
	defer tx.Rollback()

	// The customers are read before their conversations are gone: afterwards
	// nothing links them to the tenant any more.
	rows, err := tx.QueryContext(ctx,
		`SELECT DISTINCT customer_id FROM conversations WHERE product_line_id = $1`, id)
	if err != nil {
		return TenantDataDeletion{}, fmt.Errorf("list customers of product line %s: %w", id, err)
	}
	var customerIDs []string
	for rows.Next() {
		var customerID string
		if err := rows.Scan(&customerID); err != nil {
			rows.Close()
			return TenantDataDeletion{}, fmt.Errorf("scan customer of product line %s: %w", id, err)
		}
		customerIDs = append(customerIDs, customerID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return TenantDataDeletion{}, fmt.Errorf("list customers of product line %s: %w", id, err)
	}
	rows.Close()

	var counts TenantDataDeletion
	steps := []struct {
		name  string
		count *int64
		query string
		args  []interface{}
	}{
		{"messages", &counts.Messages,
			`DELETE FROM messages WHERE conversation_id IN (SELECT id FROM conversations WHERE product_line_id = $1)`,
			[]interface{}{id}},
		{"conversations", &counts.Conversations,
			`DELETE FROM conversations WHERE product_line_id = $1`,
			[]interface{}{id}},
		{"customers", &counts.Customers,
			deleteCustomersQuery,
			[]interface{}{id, pqStringArray(customerIDs)}},
		{"channel_configs", &counts.ChannelConfigs,
			`DELETE FROM channel_configs WHERE product_line_id = $1`,
			[]interface{}{id}},
		{"channels", &counts.Channels,
			`DELETE FROM channels WHERE product_line_id = $1`,
			[]interface{}{id}},
		{"knowledge_docs", &counts.KnowledgeDocs,
			`DELETE FROM knowledge_docs WHERE product_line_id = $1`,
			[]interface{}{id}},
		{"ontology_versions", &counts.OntologyVersions,
			`DELETE FROM ontology_versions WHERE product_line_id = $1`,
			[]interface{}{id}},
		{"claim_violations", &counts.ClaimViolations,
			`DELETE FROM claim_violations WHERE product_line_id = $1`,
			[]interface{}{id}},
		{"users", &counts.Users,
			`DELETE FROM users WHERE product_line_id = $1`,
			[]interface{}{id}},
	}

	for _, step := range steps {
		res, err := tx.ExecContext(ctx, step.query, step.args...)
		if err != nil {
			return TenantDataDeletion{}, fmt.Errorf("delete %s of product line %s: %w", step.name, id, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return TenantDataDeletion{}, fmt.Errorf("delete %s of product line %s: %w", step.name, id, err)
		}
		*step.count = n
	}

	if err := tx.Commit(); err != nil {
		return TenantDataDeletion{}, fmt.Errorf("commit tenant deletion: %w", err)
	}
	return counts, nil
}
