package state

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"github.com/kefu/unica/pkg/domain"
)

// migration024Path is where the migration this file exercises lives, relative
// to this package.
const migration024Path = "../../migrations/024_ontology_proposals.sql"

// TestOntologyProposals_MigrationIsRerunnable proves 024 can be replayed
// against a database that already has it applied — the same expectation
// every file under router/migrations makes, since deployment always reapplies
// the full sequence rather than tracking what already ran.
func TestOntologyProposals_MigrationIsRerunnable(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	sql, err := os.ReadFile(filepath.FromSlash(migration024Path))
	if err != nil {
		t.Fatalf("read %s: %v", migration024Path, err)
	}
	// A database this test runs against already has every migration through
	// 024 applied (see the convention documented on testDB / the other
	// *_db_test.go files in this package); replaying it here is the
	// idempotency check itself.
	if _, err := db.ExecContext(ctx, string(sql)); err != nil {
		t.Fatalf("re-running 024_ontology_proposals.sql failed (is it idempotent?): %v", err)
	}
}

// createProposalsLine inserts a product line and returns its id, with a
// cleanup that removes it and every row this file's tests attach to it.
func createProposalsLine(t *testing.T, ctx context.Context) string {
	t.Helper()
	db := testDB(t)
	var lineID string
	name := "ontology-proposals-" + uuid.NewString()[:8]
	if err := db.QueryRowContext(ctx,
		`INSERT INTO product_lines (name, display_name) VALUES ($1, $1) RETURNING id`, name).Scan(&lineID); err != nil {
		t.Fatalf("create product line: %v", err)
	}
	t.Cleanup(func() {
		db.Exec(`DELETE FROM ontology_proposals WHERE product_line_id = $1`, lineID)
		db.Exec(`DELETE FROM audit_logs WHERE product_line_id = $1`, lineID)
		db.Exec(`DELETE FROM ontology_versions WHERE product_line_id = $1`, lineID)
		db.Exec(`DELETE FROM product_lines WHERE id = $1`, lineID)
	})
	return lineID
}

// TestOntologyProposals_TierCRefusesAValue pins the CHECK that keeps a
// missing-fact ticket from ever carrying phrase or payload — the database's
// own copy of the rule pkg/domain.InsertProposal already enforces in Go, so
// a row inserted by any means other than InsertProposal is still caught.
func TestOntologyProposals_TierCRefusesAValue(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	lineID := createProposalsLine(t, ctx)

	_, err := db.ExecContext(ctx, `
		INSERT INTO ontology_proposals (product_line_id, tier, kind, phrase, missing_fact)
		VALUES ($1, 'C', 'missing_fact', 'a value snuck in', '{"property":"p","declared":false,"evidence":["c-1"],"reason":"r"}')`,
		lineID)
	if err == nil {
		t.Fatal("a tier C row carrying a phrase must be refused by the database")
	}

	_, err = db.ExecContext(ctx, `
		INSERT INTO ontology_proposals (product_line_id, tier, kind, missing_fact)
		VALUES ($1, 'C', 'missing_fact', '{"property":"p","declared":false,"evidence":["c-1"],"reason":"r"}')`,
		lineID)
	if err != nil {
		t.Errorf("a clean tier C row must be accepted: %v", err)
	}
}

// TestOntologyProposals_TierARequiresGate pins the gate requirement: an alias
// cannot be queued without a validation verdict already attached.
func TestOntologyProposals_TierARequiresGate(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	lineID := createProposalsLine(t, ctx)

	_, err := db.ExecContext(ctx, `
		INSERT INTO ontology_proposals (product_line_id, tier, kind, ref, phrase)
		VALUES ($1, 'A', 'alias', 'property:x', '能退多少')`, lineID)
	if err == nil {
		t.Fatal("a tier A row with no gate must be refused by the database")
	}

	_, err = db.ExecContext(ctx, `
		INSERT INTO ontology_proposals (product_line_id, tier, kind, ref, phrase, gate)
		VALUES ($1, 'A', 'alias', 'property:x', '能退多少', '{"pass":true}')`, lineID)
	if err != nil {
		t.Errorf("a gated tier A row must be accepted: %v", err)
	}
}

// TestOntologyProposals_TierBKindsMatchTier pins the third shape: only
// dimension_value and requires may be tier B, and nothing else may.
func TestOntologyProposals_TierBKindsMatchTier(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	lineID := createProposalsLine(t, ctx)

	if _, err := db.ExecContext(ctx, `
		INSERT INTO ontology_proposals (product_line_id, tier, kind, payload)
		VALUES ($1, 'A', 'dimension_value', '{"dimension":"d","value":"v"}')`, lineID); err == nil {
		t.Error("dimension_value claiming tier A must be refused")
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO ontology_proposals (product_line_id, tier, kind, payload)
		VALUES ($1, 'B', 'dimension_value', '{"dimension":"d","value":"v"}')`, lineID); err != nil {
		t.Errorf("a clean tier B dimension_value row must be accepted: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO ontology_proposals (product_line_id, tier, kind, payload)
		VALUES ($1, 'B', 'requires', '{"property":"p","companion":"c"}')`, lineID); err != nil {
		t.Errorf("a clean tier B requires row must be accepted: %v", err)
	}
}

// TestOntologyProposals_PendingDedup pins the partial unique index: two
// pending tier A proposals for the same ref+phrase collide, but the same
// pair does not collide once the first has moved out of pending.
func TestOntologyProposals_PendingDedup(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	lineID := createProposalsLine(t, ctx)

	insert := func() error {
		_, err := db.ExecContext(ctx, `
			INSERT INTO ontology_proposals (product_line_id, tier, kind, ref, phrase, gate)
			VALUES ($1, 'A', 'alias', 'property:x', '能退多少', '{"pass":true}')`, lineID)
		return err
	}
	if err := insert(); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if err := insert(); err == nil {
		t.Fatal("a second pending proposal for the same ref+phrase must be refused")
	}
	if _, err := db.ExecContext(ctx,
		`UPDATE ontology_proposals SET status = 'rejected' WHERE product_line_id = $1`, lineID); err != nil {
		t.Fatalf("reject: %v", err)
	}
	if err := insert(); err != nil {
		t.Errorf("re-proposing after the first was rejected must be accepted: %v", err)
	}
}

// baseOntology is a minimal, valid ontology to publish and propose against.
// It is parsed from the same YAML it is published with, as every real publish
// is: ApproveAndPublish patches the stored source and checks the result parses
// back to the candidate, so a fixture whose struct and source disagree (a
// "version:" line the struct lacks, say) would fail for the fixture's sake.
func baseOntology(name string) *domain.Ontology {
	return mustParseYAML(sprintfYAML(name))
}

func mustParseYAML(src string) *domain.Ontology {
	o, err := domain.ParseYAML([]byte(src))
	if err != nil {
		panic(err)
	}
	return o
}

const baseOntologySourceYAML = `
product_line: %s
version: 1
properties:
  服务费:
    label: 服务费
    range: {type: money, unit: yuan}
    functional: true
assertions:
  - values: {服务费: "1000"}
`

// TestApproveAndPublish_PublishesAliasAndAdvancesVersion is the acceptance
// case the build item names: approving a queued tier A proposal bumps the
// active version by one and the new version's source YAML carries the alias.
func TestApproveAndPublish_PublishesAliasAndAdvancesVersion(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	lineID := createProposalsLine(t, ctx)

	store := domain.NewStore(db, 0)
	name := "approve-publish-" + lineID[:8]
	o := baseOntology(name)
	sourceYAML := sprintfYAML(name)

	v1, err := store.Publish(ctx, lineID, o, sourceYAML, "v1")
	if err != nil {
		t.Fatalf("publish v1: %v", err)
	}
	if v1 != 1 {
		t.Fatalf("v1 = %d, want 1", v1)
	}

	id, err := store.InsertProposal(ctx, domain.Proposal{
		ProductLineID: lineID,
		Kind:          domain.ProposalKindAlias,
		Ref:           domain.PropertyAlias("服务费"),
		Phrase:        "留学服务费",
		Gate:          json.RawMessage(`{"pass":true}`),
		Evidence:      []domain.ProposalEvidence{{ConversationID: "c-1", Excerpt: "留学服务费多少钱"}},
		Occurrences:   3,
	})
	if err != nil {
		t.Fatalf("InsertProposal: %v", err)
	}

	version, err := store.ApproveAndPublish(ctx, lineID, []int64{id}, "reviewer@example.com", "看起来没问题")
	if err != nil {
		t.Fatalf("ApproveAndPublish: %v", err)
	}
	if version != 2 {
		t.Errorf("published version = %d, want 2 (one more than v1)", version)
	}

	active, activeVersion, err := store.Active(ctx, lineID)
	if err != nil {
		t.Fatalf("load active ontology: %v", err)
	}
	if activeVersion != 2 {
		t.Errorf("active version = %d, want 2", activeVersion)
	}
	if got := active.Aliases[domain.PropertyAlias("服务费")]; len(got) != 1 || got[0] != "留学服务费" {
		t.Errorf("active ontology aliases = %v, want [留学服务费]", got)
	}

	newSourceYAML, err := store.SourceYAML(ctx, lineID, 2)
	if err != nil {
		t.Fatalf("load source yaml: %v", err)
	}
	rendered, err := domain.ParseYAML([]byte(newSourceYAML))
	if err != nil {
		t.Fatalf("the published source yaml must itself parse: %v", err)
	}
	if got := rendered.Aliases[domain.PropertyAlias("服务费")]; len(got) != 1 || got[0] != "留学服务费" {
		t.Errorf("published source yaml aliases = %v, want [留学服务费]", got)
	}

	proposal, err := store.GetProposal(ctx, id)
	if err != nil {
		t.Fatalf("GetProposal: %v", err)
	}
	if proposal.Status != domain.ProposalPublished {
		t.Errorf("status = %s, want published", proposal.Status)
	}
	if proposal.PublishedVersion != 2 {
		t.Errorf("published_version = %d, want 2", proposal.PublishedVersion)
	}
	if proposal.DecidedBy != "reviewer@example.com" {
		t.Errorf("decided_by = %q", proposal.DecidedBy)
	}
}

// TestApproveAndPublish_RefusesTierC checks the boundary from the Go side
// against a real database: a tier C proposal can never reach Publish, however
// it is presented to ApproveAndPublish.
func TestApproveAndPublish_RefusesTierC(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	lineID := createProposalsLine(t, ctx)

	store := domain.NewStore(db, 0)
	name := "approve-publish-c-" + lineID[:8]
	if _, err := store.Publish(ctx, lineID, baseOntology(name), sprintfYAML(name), "v1"); err != nil {
		t.Fatalf("publish v1: %v", err)
	}

	id, err := store.InsertProposal(ctx, domain.Proposal{
		ProductLineID: lineID,
		Kind:          domain.ProposalKindMissingFact,
		MissingFact:   &domain.MissingFact{Property: "退款周期", Reason: "客户多次问及", Evidence: []string{"c-1"}},
	})
	if err != nil {
		t.Fatalf("InsertProposal: %v", err)
	}

	if _, err := store.ApproveAndPublish(ctx, lineID, []int64{id}, "reviewer@example.com", ""); err == nil {
		t.Fatal("ApproveAndPublish must refuse a tier C proposal")
	}

	// Untouched: still pending, no version.
	proposal, err := store.GetProposal(ctx, id)
	if err != nil {
		t.Fatalf("GetProposal: %v", err)
	}
	if proposal.Status != domain.ProposalPending {
		t.Errorf("status = %s, want pending (refused, not partially applied)", proposal.Status)
	}
}

// TestDecideProposals_ResolveAndReject exercises the two decisions that never
// touch the ontology.
func TestDecideProposals_ResolveAndReject(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	lineID := createProposalsLine(t, ctx)

	store := domain.NewStore(db, 0)
	name := "decide-" + lineID[:8]
	if _, err := store.Publish(ctx, lineID, baseOntology(name), sprintfYAML(name), "v1"); err != nil {
		t.Fatalf("publish v1: %v", err)
	}

	aliasID, err := store.InsertProposal(ctx, domain.Proposal{
		ProductLineID: lineID, Kind: domain.ProposalKindAlias,
		Ref: domain.PropertyAlias("服务费"), Phrase: "留学服务费",
		Gate: json.RawMessage(`{"pass":true}`),
	})
	if err != nil {
		t.Fatalf("InsertProposal alias: %v", err)
	}
	factID, err := store.InsertProposal(ctx, domain.Proposal{
		ProductLineID: lineID, Kind: domain.ProposalKindMissingFact,
		MissingFact: &domain.MissingFact{Property: "退款周期", Reason: "r", Evidence: []string{"c-1"}},
	})
	if err != nil {
		t.Fatalf("InsertProposal missing_fact: %v", err)
	}

	if _, err := store.DecideProposals(ctx, lineID, []int64{aliasID}, domain.ProposalResolved, "r@x.com", ""); err == nil {
		t.Error("resolving a tier A proposal must be refused")
	}

	updated, err := store.DecideProposals(ctx, lineID, []int64{aliasID}, domain.ProposalRejected, "r@x.com", "不需要")
	if err != nil {
		t.Fatalf("reject alias: %v", err)
	}
	if len(updated) != 1 || updated[0].Status != domain.ProposalRejected {
		t.Errorf("rejected proposal = %+v", updated)
	}

	updated, err = store.DecideProposals(ctx, lineID, []int64{factID}, domain.ProposalResolved, "r@x.com", "已在本体页补充")
	if err != nil {
		t.Fatalf("resolve missing_fact: %v", err)
	}
	if len(updated) != 1 || updated[0].Status != domain.ProposalResolved {
		t.Errorf("resolved proposal = %+v", updated)
	}
}

func sprintfYAML(name string) string {
	return fmt.Sprintf(baseOntologySourceYAML, name)
}

// ontologyWithRefundNote is baseOntology plus one more declared property, used
// to stand in for a hand-authored edit published by someone else.
func ontologyWithRefundNote(name string) *domain.Ontology {
	return mustParseYAML(fmt.Sprintf(refundNoteSourceYAML, name))
}

const refundNoteSourceYAML = `
product_line: %s
version: 1
properties:
  服务费:
    label: 服务费
    range: {type: money, unit: yuan}
    functional: true
  退款说明:
    label: 退款说明
    range: {type: string}
assertions:
  - values: {服务费: "1000"}
  - values: {退款说明: "七个工作日内原路退回"}
`

// TestApproveAndPublish_DoesNotClobberAConcurrentManualPublish reproduces the
// multi-replica race ApproveAndPublish's cache-invalidate guards against: two
// admin pods each hold their own in-process Store, so one publishing (through
// the ordinary facts-editing path) does not clear the other's cached active
// ontology. Without invalidating before the read, the second Store would
// build the approved alias on top of the version it had cached — silently
// discarding the property the first Store just published — instead of on top
// of what is actually active.
func TestApproveAndPublish_DoesNotClobberAConcurrentManualPublish(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	lineID := createProposalsLine(t, ctx)

	publisher := domain.NewStore(db, 0) // stands in for another admin replica
	reviewer := domain.NewStore(db, 0)  // the Store this test's approve call uses
	name := "no-clobber-" + lineID[:8]

	if _, err := publisher.Publish(ctx, lineID, baseOntology(name), sprintfYAML(name), "v1"); err != nil {
		t.Fatalf("publish v1: %v", err)
	}

	// reviewer's Store warms its cache on v1, the same way a replica's cache
	// would be warm from having answered a customer conversation moments ago.
	if _, _, err := reviewer.Active(ctx, lineID); err != nil {
		t.Fatalf("warm reviewer cache: %v", err)
	}

	// A different admin (publisher) hand-publishes v2 — reviewer's Store has
	// no way to know this happened; its own cache is untouched.
	if _, err := publisher.Publish(ctx, lineID, ontologyWithRefundNote(name), fmt.Sprintf(refundNoteSourceYAML, name), "v2: 补充退款说明"); err != nil {
		t.Fatalf("publish v2: %v", err)
	}

	id, err := reviewer.InsertProposal(ctx, domain.Proposal{
		ProductLineID: lineID,
		Kind:          domain.ProposalKindAlias,
		Ref:           domain.PropertyAlias("服务费"),
		Phrase:        "留学服务费",
		Gate:          json.RawMessage(`{"pass":true}`),
	})
	if err != nil {
		t.Fatalf("InsertProposal: %v", err)
	}

	version, err := reviewer.ApproveAndPublish(ctx, lineID, []int64{id}, "reviewer@example.com", "")
	if err != nil {
		t.Fatalf("ApproveAndPublish: %v", err)
	}
	if version != 3 {
		t.Fatalf("published version = %d, want 3 (built on top of v2, not the reviewer's stale v1)", version)
	}

	active, _, err := publisher.Active(ctx, lineID)
	if err != nil {
		t.Fatalf("load active ontology: %v", err)
	}
	if _, ok := active.Properties["退款说明"]; !ok {
		t.Error("v3 lost 退款说明 — ApproveAndPublish built on top of a stale cached ontology and clobbered the concurrent manual publish")
	}
	if got := active.Aliases[domain.PropertyAlias("服务费")]; len(got) != 1 || got[0] != "留学服务费" {
		t.Errorf("v3 aliases = %v, want [留学服务费]", got)
	}
}
