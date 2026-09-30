package domain

import (
	"context"
	"fmt"
)

// CountPendingProposals counts one product line's pending ontology
// proposals — the review queue depth a reviewer has to work through.
//
// It is a plain count(*) rather than ListProposals with a discarded page:
// a caller that only wants the number (the operator digest, a badge on a
// nav item) should not pay to materialise and scan a page of rows just to
// throw them away, and should not have to pick a limit that happens to be
// larger than the queue to get a true total.
func (s *Store) CountPendingProposals(ctx context.Context, productLineID string) (int, error) {
	if s == nil || s.db == nil || productLineID == "" {
		return 0, nil
	}
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM ontology_proposals WHERE product_line_id = $1 AND status = $2`,
		productLineID, string(ProposalPending)).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count pending proposals: %w", err)
	}
	return n, nil
}
