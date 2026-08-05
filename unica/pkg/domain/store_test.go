package domain

import (
	"context"
	"reflect"
	"strings"
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

// TestValidReviewStatus pins the vocabulary the database enforces separately with
// a CHECK constraint. The two lists are written in different languages and cannot
// be shared, so this test is what notices when one of them moves.
func TestValidReviewStatus(t *testing.T) {
	accepted := []string{ReviewPending, ReviewOntologyWrong, ReviewModelWrong, ReviewFalsePositive}
	for _, s := range accepted {
		if !ValidReviewStatus(s) {
			t.Errorf("ValidReviewStatus(%q) = false, want true", s)
		}
	}

	// The literal values are themselves the contract: they are stored in the
	// column, matched by the CHECK constraint, and grouped by every query that
	// turns review outcomes into a decision about what to fix.
	want := []string{"pending", "ontology_wrong", "model_wrong", "false_positive"}
	if !reflect.DeepEqual(accepted, want) {
		t.Errorf("review status constants = %q, want %q", accepted, want)
	}

	rejected := []string{"", " ", "pending ", "Pending", "PENDING", "resolved",
		"ontology-wrong", "ontology_wrong'"}
	for _, s := range rejected {
		if ValidReviewStatus(s) {
			t.Errorf("ValidReviewStatus(%q) = true, want false", s)
		}
	}
}

func TestViolationPage(t *testing.T) {
	cases := []struct {
		name                  string
		limit, offset         int
		wantLimit, wantOffset int
	}{
		{"an unset limit falls back to the default", 0, 0, DefaultViolationLimit, 0},
		{"a negative limit falls back to the default", -1, 0, DefaultViolationLimit, 0},
		{"an oversized page is capped", 100000, 0, MaxViolationLimit, 0},
		{"the cap itself is allowed", MaxViolationLimit, 0, MaxViolationLimit, 0},
		// PostgreSQL rejects a negative OFFSET outright, so clamping it here is
		// the difference between an empty page and a failed request.
		{"a negative offset never reaches the database", 10, -5, 10, 0},
		{"a usable page is left alone", 25, 75, 25, 75},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			limit, offset := violationPage(tc.limit, tc.offset)
			if limit != tc.wantLimit || offset != tc.wantOffset {
				t.Errorf("violationPage(%d, %d) = (%d, %d), want (%d, %d)",
					tc.limit, tc.offset, limit, offset, tc.wantLimit, tc.wantOffset)
			}
		})
	}
}

func TestViolationWhere(t *testing.T) {
	enforced, shadow := true, false

	cases := []struct {
		name     string
		filter   ViolationFilter
		wantSQL  string
		wantArgs []any
	}{
		{
			name:     "the product line is always part of the predicate",
			wantSQL:  " WHERE product_line_id = $1",
			wantArgs: []any{"line-1"},
		},
		{
			name:     "kind",
			filter:   ViolationFilter{Kind: string(ViolationRange)},
			wantSQL:  " WHERE product_line_id = $1 AND kind = $2",
			wantArgs: []any{"line-1", string(ViolationRange)},
		},
		{
			name:     "review status",
			filter:   ViolationFilter{ReviewStatus: ReviewPending},
			wantSQL:  " WHERE product_line_id = $1 AND review_status = $2",
			wantArgs: []any{"line-1", "pending"},
		},
		{
			// The tri-state is the point: false is a filter for shadow-mode rows,
			// not the absence of one.
			name:     "enforced false filters rather than falling through",
			filter:   ViolationFilter{Enforced: &shadow},
			wantSQL:  " WHERE product_line_id = $1 AND enforced = $2",
			wantArgs: []any{"line-1", false},
		},
		{
			name:     "every filter numbers its own placeholder",
			filter:   ViolationFilter{Kind: string(ViolationRange), ReviewStatus: ReviewModelWrong, Enforced: &enforced},
			wantSQL:  " WHERE product_line_id = $1 AND kind = $2 AND review_status = $3 AND enforced = $4",
			wantArgs: []any{"line-1", string(ViolationRange), "model_wrong", true},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clause, args := violationWhere("line-1", tc.filter)
			if clause != tc.wantSQL {
				t.Errorf("clause = %q, want %q", clause, tc.wantSQL)
			}
			if !reflect.DeepEqual(args, tc.wantArgs) {
				t.Errorf("args = %#v, want %#v", args, tc.wantArgs)
			}
		})
	}
}

// TestViolationWhere_ValuesTravelAsParameters guards the property that the clause
// is concatenated but the values are not: every filter here arrives from a query
// string, so a value that ever reaches the SQL text is an injection.
func TestViolationWhere_ValuesTravelAsParameters(t *testing.T) {
	hostile := "x' OR 1=1 --"

	clause, args := violationWhere(hostile, ViolationFilter{Kind: hostile, ReviewStatus: hostile})
	if strings.Contains(clause, hostile) {
		t.Fatalf("a filter value was formatted into the SQL: %q", clause)
	}
	if strings.ContainsAny(clause, "'\"") {
		t.Errorf("clause contains a quoted literal and so is building values, not placeholders: %q", clause)
	}
	if len(args) != 3 {
		t.Fatalf("got %d args, want 3", len(args))
	}
	for i, a := range args {
		if a != hostile {
			t.Errorf("arg %d = %#v, want the value passed in", i, a)
		}
	}
}
