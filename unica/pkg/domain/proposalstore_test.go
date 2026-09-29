package domain

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// These tests exercise every rejection this file can reach before it opens a
// connection — the same convention TestStore_PublishRejectsInvalidOntology
// relies on for Publish: a database that is nil would panic on the write, so
// a test that returns an error instead of panicking is proof the validation
// ran and stopped there. Anything that needs a row round-tripped through
// Postgres (InsertProposal succeeding, DecideProposals, ApproveAndPublish
// actually publishing) belongs in router/internal/state's
// ROUTER_TEST_POSTGRES_URL-gated tests, not here.

func TestExpectedTier(t *testing.T) {
	cases := []struct {
		kind ProposalKind
		want Tier
		ok   bool
	}{
		{ProposalKindAlias, TierAlias, true},
		{ProposalKindDimensionValue, TierDimension, true},
		{ProposalKindRequires, TierDimension, true},
		{ProposalKindMissingFact, TierMissingFact, true},
		{ProposalKind("bogus"), TierNone, false},
	}
	for _, tc := range cases {
		got, ok := expectedTier(tc.kind)
		if got != tc.want || ok != tc.ok {
			t.Errorf("expectedTier(%q) = %q, %v; want %q, %v", tc.kind, got, ok, tc.want, tc.ok)
		}
	}
}

func TestValidProposalDecision(t *testing.T) {
	cases := map[ProposalStatus]bool{
		ProposalRejected:   true,
		ProposalResolved:   true,
		ProposalApproved:   false,
		ProposalPending:    false,
		ProposalPublished:  false,
		ProposalSuperseded: false,
	}
	for status, want := range cases {
		if got := ValidProposalDecision(status); got != want {
			t.Errorf("ValidProposalDecision(%q) = %v, want %v", status, got, want)
		}
	}
}

func TestInsertProposal_RejectsMissingProductLine(t *testing.T) {
	s := NewStore(nil, 0)
	_, err := s.InsertProposal(context.Background(), Proposal{Kind: ProposalKindAlias})
	if err == nil {
		t.Fatal("a proposal with no product line must be refused")
	}
}

func TestInsertProposal_RejectsUnknownKind(t *testing.T) {
	s := NewStore(nil, 0)
	_, err := s.InsertProposal(context.Background(), Proposal{
		ProductLineID: "pl-1", Kind: ProposalKind("bogus"),
	})
	if err == nil {
		t.Fatal("an unknown kind must be refused")
	}
}

func TestInsertProposal_RejectsTierMismatch(t *testing.T) {
	s := NewStore(nil, 0)
	_, err := s.InsertProposal(context.Background(), Proposal{
		ProductLineID: "pl-1", Kind: ProposalKindAlias, Tier: TierMissingFact,
		Ref: PropertyAlias("x"), Phrase: "测试短语", Gate: json.RawMessage(`{}`),
	})
	if err == nil {
		t.Fatal("an alias proposal claiming tier C must be refused")
	}
}

// TestInsertProposal_AliasRequiresGate pins the schema's central discipline
// from the Go side: an alias with no gate verdict is refused before this
// function ever asks whether an active ontology exists, exactly the order
// migration 024's tier_a_gate constraint enforces again in SQL.
func TestInsertProposal_AliasRequiresGate(t *testing.T) {
	s := NewStore(nil, 0)
	_, err := s.InsertProposal(context.Background(), Proposal{
		ProductLineID: "pl-1", Kind: ProposalKindAlias,
		Ref: PropertyAlias("x"), Phrase: "测试短语",
	})
	if err == nil {
		t.Fatal("an alias with no gate verdict must be refused")
	}
}

func TestInsertProposal_RejectsWithoutActiveOntology(t *testing.T) {
	s := NewStore(nil, 0) // no database: Active always returns nil, nil.
	ctx := context.Background()

	cases := []struct {
		name string
		p    Proposal
	}{
		{"alias", Proposal{
			ProductLineID: "pl-1", Kind: ProposalKindAlias,
			Ref: PropertyAlias("x"), Phrase: "测试短语", Gate: json.RawMessage(`{"pass":true}`),
		}},
		{"dimension_value", Proposal{
			ProductLineID: "pl-1", Kind: ProposalKindDimensionValue,
			Payload: json.RawMessage(`{"dimension":"d","value":"v"}`),
		}},
		{"requires", Proposal{
			ProductLineID: "pl-1", Kind: ProposalKindRequires,
			Payload: json.RawMessage(`{"property":"p","companion":"c"}`),
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.InsertProposal(ctx, tc.p); err == nil {
				t.Fatal("a proposal against a line with no active ontology must be refused")
			}
		})
	}
}

func TestInsertProposal_MissingFactRequiresProperty(t *testing.T) {
	s := NewStore(nil, 0)
	ctx := context.Background()

	if _, err := s.InsertProposal(ctx, Proposal{
		ProductLineID: "pl-1", Kind: ProposalKindMissingFact, MissingFact: nil,
	}); err == nil {
		t.Fatal("a missing_fact proposal with no ticket must be refused")
	}
	if _, err := s.InsertProposal(ctx, Proposal{
		ProductLineID: "pl-1", Kind: ProposalKindMissingFact,
		MissingFact: &MissingFact{Property: "  "},
	}); err == nil {
		t.Fatal("a missing_fact ticket with a blank property must be refused")
	}
}

// TestInsertProposal_DimensionValuePayloadMustDecode and its requires sibling
// check the decode error surfaces before InsertProposal ever reaches
// ApplyDimensionValue/ApplyRequires — both need an active ontology, which
// this store (no database) can never produce, so the decode failure path is
// the only one reachable here; the "malformed payload against a real
// ontology" case is exercised in ontology_proposals_db_test.go.
func TestInsertProposal_PayloadMustBeValidJSON(t *testing.T) {
	s := NewStore(nil, 0)
	ctx := context.Background()

	// No active ontology short-circuits before JSON decoding for these two
	// kinds today (see InsertProposal's ordering), so this only documents
	// that a store with no ontology still refuses rather than panicking on
	// an empty payload.
	if _, err := s.InsertProposal(ctx, Proposal{
		ProductLineID: "pl-1", Kind: ProposalKindDimensionValue, Payload: nil,
	}); err == nil {
		t.Fatal("expected a refusal")
	}
}

func TestDecideProposals_RejectsEmptyIDs(t *testing.T) {
	s := NewStore(nil, 0)
	if _, err := s.DecideProposals(context.Background(), "pl-1", nil, ProposalRejected, "r", ""); err == nil {
		t.Fatal("an empty id list must be refused")
	}
}

func TestDecideProposals_RejectsDecisionsAReviewerCannotSetDirectly(t *testing.T) {
	s := NewStore(nil, 0)
	ctx := context.Background()
	for _, status := range []ProposalStatus{ProposalApproved, ProposalPending, ProposalPublished, ProposalSuperseded} {
		if _, err := s.DecideProposals(ctx, "pl-1", []int64{1}, status, "r", ""); err == nil {
			t.Errorf("status %q must be refused as a direct decision", status)
		}
	}
}

func TestApproveAndPublish_RejectsEmptyIDs(t *testing.T) {
	s := NewStore(nil, 0)
	if _, err := s.ApproveAndPublish(context.Background(), "pl-1", nil, "r", ""); err == nil {
		t.Fatal("an empty id list must be refused")
	}
}

func TestApproveAndPublish_RejectsNoActiveOntology(t *testing.T) {
	s := NewStore(nil, 0) // Active always returns nil for a store with no database.
	if _, err := s.ApproveAndPublish(context.Background(), "pl-1", []int64{1}, "r", ""); err == nil {
		t.Fatal("publishing against a line with no active ontology must be refused")
	}
}

func TestPgBigintArray(t *testing.T) {
	cases := []struct {
		in   []int64
		want string
	}{
		{nil, "{}"},
		{[]int64{7}, "{7}"},
		{[]int64{1, 2, 3}, "{1,2,3}"},
	}
	for _, tc := range cases {
		if got := pgBigintArray(tc.in); got != tc.want {
			t.Errorf("pgBigintArray(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestMissingIDs(t *testing.T) {
	got := missingIDs([]int64{1, 2, 3}, []Proposal{{ID: 2}})
	want := []int64{1, 3}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("missingIDs = %v, want %v", got, want)
	}
	if got := missingIDs([]int64{1, 2}, []Proposal{{ID: 1}, {ID: 2}}); got != nil {
		t.Errorf("missingIDs = %v, want nil when everything was found", got)
	}
}

func TestProposalPublishNote(t *testing.T) {
	if got := proposalPublishNote([]int64{3, 7}, ""); got != "采纳建议 #3, #7" {
		t.Errorf("proposalPublishNote = %q", got)
	}
	if got := proposalPublishNote([]int64{3}, "  已核实  "); got != "采纳建议 #3：已核实" {
		t.Errorf("proposalPublishNote with note = %q", got)
	}
}

// TestProposal_EvidenceRoundTrips is a light sanity check on the JSON shape
// InsertProposal writes and scanProposal reads back, independent of the
// database.
func TestProposalEvidence_JSONShape(t *testing.T) {
	ev := []ProposalEvidence{{ConversationID: "c-1", Excerpt: "能退多少"}}
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	var back []ProposalEvidence
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if len(back) != 1 || back[0].ConversationID != "c-1" || back[0].Excerpt != "能退多少" {
		t.Errorf("evidence round-trip = %+v", back)
	}
}

// TestNewStore_CacheLetsInsertProposalSeeAnActiveOntologyWithoutADatabase
// proves InsertProposal really does try the change against the active
// ontology (not just check it is non-nil) by seeding the in-process cache
// directly — same package, so the private field is reachable — instead of
// standing up Postgres for what is otherwise a pure ClassifyDiff check.
func TestInsertProposal_TriesTheChangeAgainstTheCachedActiveOntology(t *testing.T) {
	s := NewStore(nil, time.Hour)
	o := loadStudyAbroad(t)
	s.cached["pl-1"] = cacheEntry{ontology: o, version: 1, loadedAt: time.Now()}

	// A phrase that already is a dimension value's own wording cannot become
	// an alias of a different property — ApplyAlias refuses it, and
	// InsertProposal must surface that refusal rather than writing the row
	// (which would panic on this store's nil database if it got that far).
	_, err := s.InsertProposal(context.Background(), Proposal{
		ProductLineID: "pl-1", Kind: ProposalKindAlias,
		Ref: PropertyAlias("退费比例"), Phrase: "已提交材料", Gate: json.RawMessage(`{"pass":true}`),
	})
	if err == nil {
		t.Fatal("an alias colliding with a dimension value's own wording must be refused")
	}
}

func TestUniqueIDs_KeepsFirstSeenOrder(t *testing.T) {
	got := uniqueIDs([]int64{3, 1, 3, 2, 1})
	want := []int64{3, 1, 2}
	if len(got) != len(want) {
		t.Fatalf("uniqueIDs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("uniqueIDs = %v, want %v", got, want)
		}
	}
}
