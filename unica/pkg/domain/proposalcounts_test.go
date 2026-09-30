package domain

import (
	"context"
	"testing"
)

// CountPendingProposals has exactly one branch that runs before it ever opens
// a connection — the nil-store guard — and that is what these tests exercise,
// the same convention TestStore_PublishRejectsInvalidOntology and
// TestInsertProposal_RejectsMissingProductLine rely on: a database that is nil
// would panic on a real query, so returning (0, nil) instead of panicking is
// proof the guard ran and stopped there. A count that actually reaches
// Postgres belongs in router/internal/state's ROUTER_TEST_POSTGRES_URL-gated
// tests, not here.

func TestCountPendingProposals_NilStoreReturnsZero(t *testing.T) {
	var s *Store
	n, err := s.CountPendingProposals(context.Background(), "line-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 0 {
		t.Fatalf("CountPendingProposals(nil store) = %d, want 0", n)
	}
}

func TestCountPendingProposals_NoDatabaseReturnsZero(t *testing.T) {
	s := NewStore(nil, 0)
	n, err := s.CountPendingProposals(context.Background(), "line-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 0 {
		t.Fatalf("CountPendingProposals(no db) = %d, want 0", n)
	}
}

func TestCountPendingProposals_EmptyProductLineReturnsZero(t *testing.T) {
	s := NewStore(nil, 0)
	n, err := s.CountPendingProposals(context.Background(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 0 {
		t.Fatalf("CountPendingProposals(empty product line) = %d, want 0", n)
	}
}
