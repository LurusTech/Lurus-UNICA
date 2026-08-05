package domain

import (
	"context"
	"testing"
	"time"
)

// TestStore_AbsentIsNotAnError pins the property that makes the whole feature
// opt-in: a customer who has not authored an ontology gets nil, not a failure,
// and every caller treats nil as "skip". Without this the router would have to
// special-case adoption everywhere.
func TestStore_AbsentIsNotAnError(t *testing.T) {
	ctx := context.Background()

	cases := map[string]*Store{
		"nil store":     nil,
		"no database":   NewStore(nil, time.Minute),
		"empty line id": NewStore(nil, 0),
	}
	for name, s := range cases {
		id := "some-product-line"
		if name == "empty line id" {
			id = ""
		}
		o, version, err := s.Active(ctx, id)
		if err != nil {
			t.Errorf("%s: unexpected error %v", name, err)
		}
		if o != nil || version != 0 {
			t.Errorf("%s: expected no ontology, got v%d", name, version)
		}
	}
}

func TestStore_NilSafety(t *testing.T) {
	var s *Store
	s.Invalidate("anything")
	s.RecordViolations(context.Background(), "conv", "line", 1,
		[]Violation{{Kind: ViolationRange, Message: "x"}}, true)

	// A store without a database must also swallow both calls.
	live := NewStore(nil, 0)
	live.Invalidate("anything")
	live.RecordViolations(context.Background(), "conv", "line", 1,
		[]Violation{{Kind: ViolationRange, Message: "x"}}, false)
}

// TestStore_PublishRejectsInvalidOntology proves validation happens before the
// database is touched: an ontology that cannot be loaded must never become the
// active version, because the router would then have no facts at all.
func TestStore_PublishRejectsInvalidOntology(t *testing.T) {
	// A nil database would panic on BeginTx, so reaching the transaction at all
	// is what this test detects.
	s := NewStore(nil, 0)

	invalid := &Ontology{
		ProductLine: "T",
		Classes:     map[string]Class{"A": {Label: "a", SubclassOf: "Ghost"}},
		Properties:  map[string]Property{"p": {Label: "p", Range: Range{Type: RangeString}}},
	}

	_, err := s.Publish(context.Background(), "line-id", invalid, "", "")
	if err == nil {
		t.Fatal("an invalid ontology must be rejected")
	}
}

func TestNewStore_DefaultsTTL(t *testing.T) {
	if got := NewStore(nil, 0).ttl; got != DefaultCacheTTL {
		t.Errorf("ttl = %v, want %v", got, DefaultCacheTTL)
	}
	if got := NewStore(nil, -time.Second).ttl; got != DefaultCacheTTL {
		t.Errorf("a negative ttl must fall back to the default, got %v", got)
	}
	if got := NewStore(nil, time.Minute).ttl; got != time.Minute {
		t.Errorf("ttl = %v, want 1m", got)
	}
}
