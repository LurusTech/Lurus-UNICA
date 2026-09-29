package domain

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// This file is the persistence half of proposal.go's tier discipline: a
// candidate change a mining job found, queued for a person to approve, and
// the one call (ApproveAndPublish) that is allowed to turn an approval into a
// published ontology. Migration 024 restates the same tiers as CHECK
// constraints; InsertProposal restates them again here, before a row is ever
// written, so a caller gets a Go error naming what is wrong rather than a
// driver error off a constraint it cannot see.

// ProposalKind names what a proposal changes. It determines which of ref,
// phrase, payload or missing_fact the row carries, and which tier it must be.
type ProposalKind string

const (
	// ProposalKindAlias is tier A: ref + phrase, nothing else.
	ProposalKindAlias ProposalKind = "alias"
	// ProposalKindDimensionValue is tier B: payload holds {dimension,value,label}.
	ProposalKindDimensionValue ProposalKind = "dimension_value"
	// ProposalKindRequires is tier B: payload holds {property,companion}.
	ProposalKindRequires ProposalKind = "requires"
	// ProposalKindMissingFact is tier C: missing_fact holds the ticket, and
	// nothing on the row can carry a value.
	ProposalKindMissingFact ProposalKind = "missing_fact"
)

// ProposalStatus is where a proposal sits in the review queue.
type ProposalStatus string

const (
	ProposalPending    ProposalStatus = "pending"
	ProposalApproved   ProposalStatus = "approved"
	ProposalRejected   ProposalStatus = "rejected"
	ProposalSuperseded ProposalStatus = "superseded"
	ProposalPublished  ProposalStatus = "published"
	// ProposalResolved is tier C only: a person added the fact to the
	// ontology by hand and is closing the ticket that asked for it. Nothing
	// in this file writes an ontology change for a resolved row — the fact
	// went in through the ordinary facts-editing path, not through here.
	ProposalResolved ProposalStatus = "resolved"
)

// ErrProposalPending is InsertProposal's answer when an identical proposal is
// already waiting in the queue (migration 024's pending dedup index). A mining
// round repeated over overlapping evidence finds the same things again; that
// is not a failure, and callers skip rather than report it.
var ErrProposalPending = errors.New("an identical proposal is already pending review")

// ValidProposalDecision reports whether status is one a reviewer may set
// directly (approved is reached only through ApproveAndPublish, and pending
// is where a row starts, so neither is a decision a caller makes).
func ValidProposalDecision(status ProposalStatus) bool {
	return status == ProposalRejected || status == ProposalResolved
}

// ProposalEvidence is one redacted excerpt supporting a proposal. The text
// here is already what an export produced, never a raw conversation.
type ProposalEvidence struct {
	ConversationID string `json:"conversation_id"`
	Excerpt        string `json:"excerpt,omitempty"`
}

// DimensionValuePayload is a tier B dimension_value proposal's payload.
type DimensionValuePayload struct {
	Dimension string `json:"dimension"`
	Value     string `json:"value"`
	Label     string `json:"label,omitempty"`
}

// RequiresPayload is a tier B requires proposal's payload.
type RequiresPayload struct {
	Property  string `json:"property"`
	Companion string `json:"companion"`
}

// Proposal is one queued ontology change, whatever tier it belongs to. Only
// the fields its kind uses are populated; see the CHECK constraints in
// migration 024 for exactly which.
type Proposal struct {
	ID            int64
	ProductLineID string
	Tier          Tier
	Kind          ProposalKind

	Ref    AliasRef // tier A only
	Phrase string   // tier A only

	Payload json.RawMessage // tier B only

	MissingFact *MissingFact // tier C only

	Evidence            []ProposalEvidence
	Occurrences         int
	Share               *float64
	BaseOntologyVersion int
	// Gate is the replay validation verdict this candidate was queued with,
	// as an opaque JSON summary — required for tier A, see InsertProposal.
	Gate json.RawMessage

	Status           ProposalStatus
	DecidedBy        string
	DecidedAt        *time.Time
	DecisionNote     string
	PublishedVersion int

	CreatedAt time.Time
	UpdatedAt time.Time
}

// ProposalFilter narrows a listing, matching ViolationFilter's shape.
type ProposalFilter struct {
	Status ProposalStatus
	Tier   Tier
	Limit  int
	Offset int
}

const (
	// DefaultProposalLimit is the page size when a caller does not ask for one.
	DefaultProposalLimit = 50
	// MaxProposalLimit caps a caller-supplied page size, for the same reason
	// ViolationFilter caps one: a review queue is read by a person.
	MaxProposalLimit = 200
)

func proposalPage(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = DefaultProposalLimit
	}
	if limit > MaxProposalLimit {
		limit = MaxProposalLimit
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

// expectedTier is the tier a kind must carry, restating migration 024's
// tier_a_gate/tier_b_kinds/tier_c_shape constraints in Go so InsertProposal
// can refuse before it ever opens a connection.
func expectedTier(kind ProposalKind) (Tier, bool) {
	switch kind {
	case ProposalKindAlias:
		return TierAlias, true
	case ProposalKindDimensionValue, ProposalKindRequires:
		return TierDimension, true
	case ProposalKindMissingFact:
		return TierMissingFact, true
	default:
		return TierNone, false
	}
}

// InsertProposal validates p against the product line's active ontology
// before writing it, trying the change through the same functions a person's
// own edit would go through:
//
//   - alias: ApplyAlias must succeed and classify as tier A.
//   - dimension_value / requires: ApplyDimensionValue / ApplyRequires must
//     succeed and classify as tier B.
//   - missing_fact: carries no value field by construction (MissingFact has
//     none), so the check is that one is present and names a property.
//
// A row that fails any of this is never written. That is what keeps a queue
// entry trustworthy: whoever mined it, ClassifyDiff — not the miner's own
// judgement of itself — is what decided its tier.
func (s *Store) InsertProposal(ctx context.Context, p Proposal) (int64, error) {
	// No nil-database guard, on purpose, matching Publish: every check above
	// the INSERT runs first, so a database that is nil in a test panics on
	// the write rather than silently accepting a row that should have been
	// refused — the same signal TestStore_PublishRejectsInvalidOntology
	// relies on for Publish.
	if p.ProductLineID == "" {
		return 0, fmt.Errorf("insert proposal: product line is required")
	}
	wantTier, ok := expectedTier(p.Kind)
	if !ok {
		return 0, fmt.Errorf("insert proposal: unknown kind %q", p.Kind)
	}
	if p.Tier != "" && p.Tier != wantTier {
		return 0, fmt.Errorf("insert proposal: kind %q must be tier %q, got %q", p.Kind, wantTier, p.Tier)
	}
	p.Tier = wantTier

	active, _, err := s.Active(ctx, p.ProductLineID)
	if err != nil {
		return 0, fmt.Errorf("insert proposal: load active ontology: %w", err)
	}

	switch p.Kind {
	case ProposalKindAlias:
		if len(p.Gate) == 0 {
			return 0, fmt.Errorf("insert proposal: tier A alias %q must carry a gate verdict before it can be queued", p.Phrase)
		}
		if active == nil {
			return 0, fmt.Errorf("insert proposal: product line %s has no active ontology to validate an alias against", p.ProductLineID)
		}
		if _, err := ApplyAlias(active, p.Ref, p.Phrase); err != nil {
			return 0, fmt.Errorf("insert proposal: %w", err)
		}
	case ProposalKindDimensionValue:
		if active == nil {
			return 0, fmt.Errorf("insert proposal: product line %s has no active ontology to validate a dimension value against", p.ProductLineID)
		}
		var payload DimensionValuePayload
		if err := json.Unmarshal(p.Payload, &payload); err != nil {
			return 0, fmt.Errorf("insert proposal: decode dimension_value payload: %w", err)
		}
		if _, err := ApplyDimensionValue(active, payload.Dimension, payload.Value, payload.Label); err != nil {
			return 0, fmt.Errorf("insert proposal: %w", err)
		}
	case ProposalKindRequires:
		if active == nil {
			return 0, fmt.Errorf("insert proposal: product line %s has no active ontology to validate a requires constraint against", p.ProductLineID)
		}
		var payload RequiresPayload
		if err := json.Unmarshal(p.Payload, &payload); err != nil {
			return 0, fmt.Errorf("insert proposal: decode requires payload: %w", err)
		}
		if _, err := ApplyRequires(active, payload.Property, payload.Companion); err != nil {
			return 0, fmt.Errorf("insert proposal: %w", err)
		}
	case ProposalKindMissingFact:
		if p.MissingFact == nil || strings.TrimSpace(p.MissingFact.Property) == "" {
			return 0, fmt.Errorf("insert proposal: missing_fact must name a property")
		}
	}

	evidence := p.Evidence
	if evidence == nil {
		evidence = []ProposalEvidence{}
	}
	evidenceJSON, err := json.Marshal(evidence)
	if err != nil {
		return 0, fmt.Errorf("insert proposal: encode evidence: %w", err)
	}
	var missingFactJSON []byte
	if p.MissingFact != nil {
		missingFactJSON, err = json.Marshal(p.MissingFact)
		if err != nil {
			return 0, fmt.Errorf("insert proposal: encode missing_fact: %w", err)
		}
	}

	var id int64
	err = s.db.QueryRowContext(ctx, `
		INSERT INTO ontology_proposals
			(product_line_id, tier, kind, ref, phrase, payload, missing_fact,
			 evidence, occurrences, share, base_ontology_version, gate, status)
		VALUES ($1, $2, $3, NULLIF($4, ''), NULLIF($5, ''), NULLIF($6, '')::jsonb, NULLIF($7, '')::jsonb,
		        $8, $9, $10, NULLIF($11, 0), NULLIF($12, '')::jsonb, 'pending')
		RETURNING id`,
		p.ProductLineID, string(p.Tier), string(p.Kind), string(p.Ref), p.Phrase,
		nullableJSON(p.Payload), nullableJSON(missingFactJSON),
		evidenceJSON, p.Occurrences, p.Share, p.BaseOntologyVersion, nullableJSON(p.Gate),
	).Scan(&id)
	if err != nil {
		// Matched on the index name because this package is standard-library
		// only and cannot see the driver's error type.
		if strings.Contains(err.Error(), "idx_ontology_proposals_pending_dedup") {
			return 0, ErrProposalPending
		}
		return 0, fmt.Errorf("insert proposal: %w", err)
	}
	return id, nil
}

// nullableJSON turns an empty/nil raw message into "" so NULLIF(...,”)::jsonb
// maps it to SQL NULL instead of storing the JSON literal "null" or an empty
// string as a value.
func nullableJSON(data json.RawMessage) string {
	if len(data) == 0 {
		return ""
	}
	return string(data)
}

const proposalColumns = `id, product_line_id, tier, kind, COALESCE(ref, ''), COALESCE(phrase, ''),
	payload, missing_fact, evidence, occurrences, share, COALESCE(base_ontology_version, 0), gate,
	status, COALESCE(decided_by, ''), decided_at, COALESCE(decision_note, ''),
	COALESCE(published_version, 0), created_at, updated_at`

func scanProposal(sc rowScanner) (Proposal, error) {
	var p Proposal
	var tier, kind, ref, phrase, status string
	var payload, missingFact, gate, evidence []byte
	var share sql.NullFloat64

	err := sc.Scan(&p.ID, &p.ProductLineID, &tier, &kind, &ref, &phrase,
		&payload, &missingFact, &evidence, &p.Occurrences, &share, &p.BaseOntologyVersion, &gate,
		&status, &p.DecidedBy, &p.DecidedAt, &p.DecisionNote,
		&p.PublishedVersion, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return Proposal{}, err
	}
	p.Tier = Tier(tier)
	p.Kind = ProposalKind(kind)
	p.Ref = AliasRef(ref)
	p.Phrase = phrase
	p.Status = ProposalStatus(status)
	if len(payload) > 0 {
		p.Payload = json.RawMessage(payload)
	}
	if len(gate) > 0 {
		p.Gate = json.RawMessage(gate)
	}
	if share.Valid {
		p.Share = &share.Float64
	}
	if len(missingFact) > 0 {
		var mf MissingFact
		if err := json.Unmarshal(missingFact, &mf); err != nil {
			return Proposal{}, fmt.Errorf("scan proposal %d: decode missing_fact: %w", p.ID, err)
		}
		p.MissingFact = &mf
	}
	if len(evidence) > 0 {
		if err := json.Unmarshal(evidence, &p.Evidence); err != nil {
			return Proposal{}, fmt.Errorf("scan proposal %d: decode evidence: %w", p.ID, err)
		}
	}
	return p, nil
}

// ListProposals returns one page of a product line's proposals, newest first,
// with the total number of rows matching the same filters — the same shape
// ListViolations uses, for a reviewer to know how large the queue is.
func (s *Store) ListProposals(ctx context.Context, productLineID string, f ProposalFilter) ([]Proposal, int, error) {
	if s == nil || s.db == nil {
		return nil, 0, fmt.Errorf("list proposals: no database")
	}
	clause := " WHERE product_line_id = $1"
	args := []any{productLineID}
	if f.Status != "" {
		args = append(args, string(f.Status))
		clause += fmt.Sprintf(" AND status = $%d", len(args))
	}
	if f.Tier != "" {
		args = append(args, string(f.Tier))
		clause += fmt.Sprintf(" AND tier = $%d", len(args))
	}

	var total int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM ontology_proposals`+clause, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count proposals: %w", err)
	}

	limit, offset := proposalPage(f.Limit, f.Offset)
	args = append(args, limit, offset)
	query := `SELECT ` + proposalColumns + ` FROM ontology_proposals` + clause +
		fmt.Sprintf(` ORDER BY created_at DESC, id DESC LIMIT $%d OFFSET $%d`, len(args)-1, len(args))

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list proposals: %w", err)
	}
	defer rows.Close()

	out := make([]Proposal, 0, limit)
	for rows.Next() {
		p, err := scanProposal(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("list proposals: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("list proposals: %w", err)
	}
	return out, total, nil
}

// GetProposal reads one proposal by id. A missing row is (nil, nil), matching
// GetViolation: only the caller knows whether absence is a 404.
func (s *Store) GetProposal(ctx context.Context, id int64) (*Proposal, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("get proposal: no database")
	}
	p, err := scanProposal(s.db.QueryRowContext(ctx,
		`SELECT `+proposalColumns+` FROM ontology_proposals WHERE id = $1`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load proposal %d: %w", id, err)
	}
	return &p, nil
}

// pgBigintArray renders an int64 slice as a PostgreSQL array literal, the way
// pkg/platformsettings.pgTextArray renders a string one — this package is
// standard-library only for the same reason given there, and proposal ids
// come from parsed JSON or a previous query, never from unescaped input.
func pgBigintArray(ids []int64) string {
	var b strings.Builder
	b.WriteByte('{')
	for i, id := range ids {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%d", id)
	}
	b.WriteByte('}')
	return b.String()
}

// uniqueIDs drops repeated ids, keeping first-seen order. A batch that names
// one proposal twice would otherwise apply its change twice and then report a
// count mismatch that reads like a concurrent decision.
func uniqueIDs(ids []int64) []int64 {
	seen := make(map[int64]bool, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// missingIDs returns the ids in want that have no matching proposal in got,
// in want's order, so a partial batch failure names exactly which rows did
// not apply instead of a bare count.
func missingIDs(want []int64, got []Proposal) []int64 {
	present := make(map[int64]bool, len(got))
	for _, p := range got {
		present[p.ID] = true
	}
	var missing []int64
	for _, id := range want {
		if !present[id] {
			missing = append(missing, id)
		}
	}
	return missing
}

// DecideProposals sets a batch of pending proposals to status in one
// transaction — reject (any tier) or resolved (tier C only, meaning a person
// added the fact by hand and is closing the ticket). It never touches the
// ontology: the only path that does is ApproveAndPublish.
//
// The UPDATE's own WHERE status = 'pending' is what makes the batch atomic
// without a separate lock-then-update step: every row it did not touch
// (already decided by someone else, or never existed) is simply absent from
// RETURNING, and finding fewer rows than ids requested rolls the whole
// transaction back rather than leaving part of the batch decided.
func (s *Store) DecideProposals(ctx context.Context, productLineID string, ids []int64, status ProposalStatus, decidedBy, note string) ([]Proposal, error) {
	ids = uniqueIDs(ids)
	if len(ids) == 0 {
		return nil, fmt.Errorf("decide proposals: no proposals selected")
	}
	if !ValidProposalDecision(status) {
		return nil, fmt.Errorf("decide proposals: %q is not a decision a reviewer sets directly", status)
	}

	var out []Proposal
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			UPDATE ontology_proposals
			SET status = $1, decided_by = NULLIF($2, ''), decided_at = NOW(),
			    decision_note = NULLIF($3, ''), updated_at = NOW()
			WHERE product_line_id = $4 AND id = ANY($5) AND status = 'pending'
			RETURNING `+proposalColumns,
			string(status), decidedBy, note, productLineID, pgBigintArray(ids))
		if err != nil {
			return fmt.Errorf("update proposals: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			p, err := scanProposal(rows)
			if err != nil {
				return fmt.Errorf("update proposals: %w", err)
			}
			out = append(out, p)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("update proposals: %w", err)
		}
		if missing := missingIDs(ids, out); len(missing) > 0 {
			return fmt.Errorf("proposal(s) %v not found for product line %s or no longer pending", missing, productLineID)
		}
		if status == ProposalResolved {
			for _, p := range out {
				if p.Tier != TierMissingFact {
					return fmt.Errorf("proposal %d is tier %s; only tier C proposals can be resolved", p.ID, p.Tier)
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// proposalPublishNote composes the audit-visible publish note ApproveAndPublish
// writes into ontology_versions.note, so a version's history says which
// proposals produced it without anyone cross-referencing ontology_proposals.
func proposalPublishNote(ids []int64, note string) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = fmt.Sprintf("#%d", id)
	}
	text := "采纳建议 " + strings.Join(parts, ", ")
	if note = strings.TrimSpace(note); note != "" {
		text += "：" + note
	}
	return text
}

// ApproveAndPublish is the only call in this package that turns an approved
// proposal into a live ontology change. It applies every selected proposal
// (tier A or B only — tier C never reaches this function, see the kind check
// below) to the product line's active ontology through the same
// ApplyAlias/ApplyDimensionValue/ApplyRequires functions a person's own edit
// goes through, re-classifies the combined result, renders it back to YAML
// preserving what a person wrote, and publishes it through the ordinary
// Store.Publish path — a second approval click away from a hand-authored
// change, never a shortcut around one.
func (s *Store) ApproveAndPublish(ctx context.Context, productLineID string, ids []int64, reviewer, note string) (int, error) {
	ids = uniqueIDs(ids)
	if len(ids) == 0 {
		return 0, fmt.Errorf("approve and publish: no proposals selected")
	}

	// Active caches its result for up to DefaultCacheTTL (five minutes) so the
	// answer path never pays a per-message query — but that is exactly wrong
	// here: a person could have hand-published a YAML edit through the facts
	// page moments ago, and building this patch on a stale cached ontology
	// would silently discard that edit the moment this call publishes on top
	// of it. Approve is a person clicking a button, not a hot path, so it pays
	// for a fresh read instead of risking someone else's just-published change.
	s.Invalidate(productLineID)
	active, activeVersion, err := s.Active(ctx, productLineID)
	if err != nil {
		return 0, fmt.Errorf("approve and publish: load active ontology: %w", err)
	}
	if active == nil {
		return 0, fmt.Errorf("approve and publish: product line %s has no active ontology", productLineID)
	}
	sourceYAML, err := s.SourceYAML(ctx, productLineID, activeVersion)
	if err != nil {
		return 0, fmt.Errorf("approve and publish: %w", err)
	}

	// Reading here does not lock: the window between this read and the
	// status='pending' write in markProposalsPublished, below, is where a
	// concurrent decision on the same id could race this one. That is an
	// acceptable gap for a human clicking approve in the admin console, not a
	// customer-facing hot path — and markProposalsPublished still refuses to
	// report success if a row it expected to flip was no longer pending by
	// the time it tried to.
	proposals, err := fetchProposalsByIDs(ctx, s.db, productLineID, ids)
	if err != nil {
		return 0, fmt.Errorf("approve and publish: %w", err)
	}
	for _, p := range proposals {
		if p.Status != ProposalPending {
			return 0, fmt.Errorf("approve and publish: proposal %d is %s, not pending", p.ID, p.Status)
		}
	}

	candidate := active
	edits := make([]YAMLEdit, 0, len(proposals))
	for _, p := range proposals {
		switch p.Kind {
		case ProposalKindAlias:
			candidate, err = ApplyAlias(candidate, p.Ref, p.Phrase)
			if err == nil {
				edits = append(edits, YAMLEdit{Kind: ProposalKindAlias, Ref: p.Ref, Phrase: p.Phrase})
			}
		case ProposalKindDimensionValue:
			var payload DimensionValuePayload
			if uerr := json.Unmarshal(p.Payload, &payload); uerr != nil {
				return 0, fmt.Errorf("approve and publish: proposal %d: decode payload: %w", p.ID, uerr)
			}
			candidate, err = ApplyDimensionValue(candidate, payload.Dimension, payload.Value, payload.Label)
			if err == nil {
				edits = append(edits, YAMLEdit{Kind: ProposalKindDimensionValue,
					Dimension: payload.Dimension, Value: payload.Value, Label: payload.Label})
			}
		case ProposalKindRequires:
			var payload RequiresPayload
			if uerr := json.Unmarshal(p.Payload, &payload); uerr != nil {
				return 0, fmt.Errorf("approve and publish: proposal %d: decode payload: %w", p.ID, uerr)
			}
			candidate, err = ApplyRequires(candidate, payload.Property, payload.Companion)
			if err == nil {
				edits = append(edits, YAMLEdit{Kind: ProposalKindRequires,
					Property: payload.Property, Companion: payload.Companion})
			}
		default:
			return 0, fmt.Errorf("approve and publish: proposal %d is tier %s and cannot be published automatically; "+
				"resolve it after adding the fact by hand", p.ID, p.Tier)
		}
		if err != nil {
			return 0, fmt.Errorf("approve and publish: proposal %d: %w", p.ID, err)
		}
	}

	if tier := ClassifyDiff(active, candidate); tier.rank() > TierDimension.rank() {
		return 0, fmt.Errorf("approve and publish: combined change classified as tier %q, want at most B", tier)
	}

	newYAML, err := RenderYAMLPatch(sourceYAML, edits)
	if err != nil {
		return 0, fmt.Errorf("approve and publish: render yaml: %w", err)
	}
	rendered, err := ParseYAML([]byte(newYAML))
	if err != nil {
		return 0, fmt.Errorf("approve and publish: generated yaml failed to parse back: %w", err)
	}
	if !sameJSON(rendered, candidate) {
		return 0, fmt.Errorf("approve and publish: generated yaml does not match the validated candidate")
	}

	version, err := s.Publish(ctx, productLineID, candidate, newYAML, proposalPublishNote(ids, note))
	if err != nil {
		return 0, fmt.Errorf("approve and publish: %w", err)
	}

	if err := s.markProposalsPublished(ctx, productLineID, ids, reviewer, note, version); err != nil {
		return version, fmt.Errorf("ontology published as v%d but marking proposals published failed: %w", version, err)
	}
	return version, nil
}

// fetchProposalsByIDs reads the requested proposals in a product line, order
// matching ids, and errors naming whichever id was not found — used for the
// pre-publish read in ApproveAndPublish, where FOR UPDATE would buy nothing
// because the row is not held locked across the YAML render and Publish call
// that follow (see the comment at its call site).
func fetchProposalsByIDs(ctx context.Context, db *sql.DB, productLineID string, ids []int64) ([]Proposal, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT `+proposalColumns+` FROM ontology_proposals WHERE product_line_id = $1 AND id = ANY($2)`,
		productLineID, pgBigintArray(ids))
	if err != nil {
		return nil, fmt.Errorf("load proposals: %w", err)
	}
	defer rows.Close()

	found := make(map[int64]Proposal, len(ids))
	for rows.Next() {
		p, err := scanProposal(rows)
		if err != nil {
			return nil, fmt.Errorf("load proposals: %w", err)
		}
		found[p.ID] = p
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load proposals: %w", err)
	}

	out := make([]Proposal, 0, len(ids))
	for _, id := range ids {
		p, ok := found[id]
		if !ok {
			return nil, fmt.Errorf("proposal %d not found for product line %s", id, productLineID)
		}
		out = append(out, p)
	}
	return out, nil
}

// markProposalsPublished flips the selected ids from pending to published.
// Filtering the UPDATE on status = 'pending' is the final guard against the
// race the read in ApproveAndPublish accepted: if a row was decided by
// someone else in between, it is not among rowsAffected and the mismatch is
// reported rather than silently claimed.
func (s *Store) markProposalsPublished(ctx context.Context, productLineID string, ids []int64, reviewer, note string, version int) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE ontology_proposals
		SET status = 'published', decided_by = NULLIF($1, ''), decided_at = NOW(),
		    decision_note = NULLIF($2, ''), published_version = $3, updated_at = NOW()
		WHERE product_line_id = $4 AND id = ANY($5) AND status = 'pending'`,
		reviewer, note, version, productLineID, pgBigintArray(ids))
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if int(n) != len(ids) {
		return fmt.Errorf("expected to mark %d proposal(s) published, marked %d; "+
			"some were no longer pending when the write ran", len(ids), n)
	}
	return nil
}
