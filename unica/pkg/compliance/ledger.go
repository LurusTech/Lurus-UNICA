// Package compliance keeps the judgement evidence ledger: one row per decision
// the router made about a customer message, chained per product line by hash
// so that a later change to any recorded row can be discovered (migration 025).
//
// The chain makes changes discoverable; it does not prevent them. Anyone with
// write access to the database can rewrite a row and recompute every hash after
// it. What they cannot do is make the recomputed chain agree with a head digest
// the customer downloaded and kept outside this system, which is why the head
// digest exists and why the console offers it for daily download.
//
// Everything here is pure: the store that writes rows lives in ledgerstore.go,
// and the admin service reads rows with its own SQL and verifies them with
// Verify, so the writer and every reader share one definition of the hash.
package compliance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/kefu/unica/pkg/domain"
)

// AuditConfigKey is the top-level config_json key holding the ledger switch.
const AuditConfigKey = "audit_ledger"

// Kinds of decision a row records.
const (
	// KindJudged is a model answer that went through the decision chain.
	KindJudged = "judged"
	// KindNoAnswer is a handoff because the model produced nothing to judge.
	KindNoAnswer = "no_answer"
	// KindPreAI is a handoff decided by triage before the model was called.
	KindPreAI = "pre_ai"
)

// ReasonClassNone marks a row whose decision was a plain send: nothing left
// the AI, so there is no handoff reason to classify. The other classes are
// domain.HandoffReasonClass values.
const ReasonClassNone = "none"

// AuditConfig is the per-product-line ledger switch. The zero value is off,
// and a line whose config_json never mentions the key records nothing.
type AuditConfig struct {
	Enabled bool `json:"enabled"`
}

// LoadAuditConfig reads the audit_ledger block of a product line's
// config_json. Anything unreadable is off: the ledger is an opt-in, and a
// malformed block must not start writing customer messages into a new table.
func LoadAuditConfig(raw json.RawMessage) AuditConfig {
	if len(raw) == 0 {
		return AuditConfig{}
	}
	var wrapper struct {
		Audit *AuditConfig `json:"audit_ledger"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil || wrapper.Audit == nil {
		return AuditConfig{}
	}
	return *wrapper.Audit
}

// AuditSource is one retrieved passage as the ledger keeps it: identified, with
// a digest of its text and never the text itself. The digest is enough to show
// later which passage an answer stood on without copying the tenant's documents
// into a table with a different retention.
type AuditSource struct {
	domain.TraceSource
	ContentSHA256 string `json:"content_sha256,omitempty"`
}

// AuditClaim is one [FACT:] assertion the model attached, with stable field
// names; domain.Claim carries no JSON tags and its Go field names are not a
// format this table should be bound to.
type AuditClaim struct {
	Property   string   `json:"property"`
	Value      string   `json:"value"`
	Qualifiers []string `json:"qualifiers,omitempty"`
}

// AuditEntry is one row of answer_audit. Fields map one to one onto the
// columns; a nil pointer is a NULL.
type AuditEntry struct {
	ID              int64
	ProductLineID   string
	Seq             int64
	ConversationID  string
	CorrelationID   string
	Kind            string
	Decision        string
	Reason          string
	ReasonClass     string
	ConfidenceScore *float64
	OntologyVersion *int
	FactsInjected   bool
	ValidationMode  string
	BreakerOpen     bool
	Enforced        bool
	// The JSON columns. Hashing re-encodes them (see canonicalJSON), so the
	// whitespace and key order Postgres gives them back in do not matter.
	Claims          json.RawMessage
	Violations      json.RawMessage
	Commitments     json.RawMessage
	Sources         json.RawMessage
	ConfigSHA256    string
	CustomerMessage string
	DeliveredAnswer *string
	WithheldAnswer  *string
	PrevHash        string
	RowHash         string
	CreatedAt       time.Time
}

// canonicalEntry fixes the field order the hash is computed over. It is a
// separate type so that adding a field to AuditEntry cannot silently change
// the hash of every existing row: a new field has to be added here, under a
// new format version, on purpose.
type canonicalEntry struct {
	Format          int             `json:"format"`
	ProductLineID   string          `json:"product_line_id"`
	Seq             int64           `json:"seq"`
	ConversationID  string          `json:"conversation_id"`
	CorrelationID   string          `json:"correlation_id"`
	Kind            string          `json:"kind"`
	Decision        string          `json:"decision"`
	Reason          string          `json:"reason"`
	ReasonClass     string          `json:"reason_class"`
	ConfidenceScore *float64        `json:"confidence_score"`
	OntologyVersion *int            `json:"ontology_version"`
	FactsInjected   bool            `json:"facts_injected"`
	ValidationMode  string          `json:"validation_mode"`
	BreakerOpen     bool            `json:"breaker_open"`
	Enforced        bool            `json:"enforced"`
	Claims          json.RawMessage `json:"claims"`
	Violations      json.RawMessage `json:"violations"`
	Commitments     json.RawMessage `json:"commitments"`
	Sources         json.RawMessage `json:"sources"`
	ConfigSHA256    string          `json:"config_sha256"`
	CustomerMessage string          `json:"customer_message"`
	DeliveredAnswer *string         `json:"delivered_answer"`
	WithheldAnswer  *string         `json:"withheld_answer"`
	CreatedAt       string          `json:"created_at"`
}

// canonicalFormat versions canonicalEntry.
const canonicalFormat = 1

// timestampLayout keeps microseconds always, matching what timestamptz stores,
// so a row read back formats exactly as it did when it was hashed.
const timestampLayout = "2006-01-02T15:04:05.000000Z"

// Canonical is the byte string a row's hash covers: every column except id,
// prev_hash and row_hash, in a fixed order. prev_hash is not in it because
// ChainHash prefixes it explicitly.
func (e AuditEntry) Canonical() []byte {
	c := canonicalEntry{
		Format:          canonicalFormat,
		ProductLineID:   e.ProductLineID,
		Seq:             e.Seq,
		ConversationID:  e.ConversationID,
		CorrelationID:   e.CorrelationID,
		Kind:            e.Kind,
		Decision:        e.Decision,
		Reason:          e.Reason,
		ReasonClass:     e.ReasonClass,
		ConfidenceScore: e.ConfidenceScore,
		OntologyVersion: e.OntologyVersion,
		FactsInjected:   e.FactsInjected,
		ValidationMode:  e.ValidationMode,
		BreakerOpen:     e.BreakerOpen,
		Enforced:        e.Enforced,
		Claims:          canonicalJSON(e.Claims),
		Violations:      canonicalJSON(e.Violations),
		Commitments:     canonicalJSON(e.Commitments),
		Sources:         canonicalJSON(e.Sources),
		ConfigSHA256:    e.ConfigSHA256,
		CustomerMessage: e.CustomerMessage,
		DeliveredAnswer: e.DeliveredAnswer,
		WithheldAnswer:  e.WithheldAnswer,
		CreatedAt:       e.CreatedAt.UTC().Truncate(time.Microsecond).Format(timestampLayout),
	}
	out, err := json.Marshal(c)
	if err != nil {
		// Every field is a plain value or already-canonical JSON, so this is
		// unreachable; panicking would take a worker down for evidence, so the
		// failure is folded into the bytes instead and the row will not verify.
		return []byte("unmarshalable:" + err.Error())
	}
	return out
}

// canonicalJSON re-encodes a JSON value into one deterministic form: object
// keys sorted, no insignificant whitespace, numbers in Go's shortest float
// form. JSONB reorders keys and drops whitespace on the way in, so hashing the
// bytes as written would make every row fail once read back. An empty value is
// the empty list the columns default to.
func canonicalJSON(raw json.RawMessage) json.RawMessage {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return json.RawMessage("[]")
	}
	var v interface{}
	if err := json.Unmarshal(raw, &v); err != nil {
		// Not JSON at all; keep it distinguishable rather than dropping it, so
		// two different broken values never hash alike.
		quoted, _ := json.Marshal(string(raw))
		return quoted
	}
	if v == nil {
		return json.RawMessage("[]")
	}
	out, err := json.Marshal(cleanJSONStrings(v))
	if err != nil {
		quoted, _ := json.Marshal(string(raw))
		return quoted
	}
	return out
}

// cleanJSONStrings drops NUL characters from every string and key: JSONB
// refuses \u0000, and a refused insert would lose the whole row. Applying it
// on the read path too is harmless, because stored values never contain one.
func cleanJSONStrings(v interface{}) interface{} {
	switch t := v.(type) {
	case string:
		return cleanText(t)
	case []interface{}:
		for i := range t {
			t[i] = cleanJSONStrings(t[i])
		}
		return t
	case map[string]interface{}:
		out := make(map[string]interface{}, len(t))
		for k, val := range t {
			out[cleanText(k)] = cleanJSONStrings(val)
		}
		return out
	}
	return v
}

// CanonicalJSON is canonicalJSON for writers: the store inserts exactly the
// bytes the hash was computed over.
func CanonicalJSON(raw json.RawMessage) json.RawMessage { return canonicalJSON(raw) }

// GenesisHash is the prev_hash of a product line's first row. Binding it to
// the product line means a chain cannot be moved under another line's id and
// still verify from its start.
func GenesisHash(productLineID string) string {
	sum := sha256.Sum256([]byte("unica-audit-genesis:" + productLineID))
	return hex.EncodeToString(sum[:])
}

// ChainHash is a row's row_hash: the hash of the previous row's hash and this
// row's canonical bytes.
func ChainHash(prev string, e AuditEntry) string {
	h := sha256.New()
	h.Write([]byte(prev))
	h.Write([]byte("\n"))
	h.Write(e.Canonical())
	return hex.EncodeToString(h.Sum(nil))
}

// Seal fills in prev_hash and row_hash for a row about to be appended after
// the given head.
func (e *AuditEntry) Seal(prevHash string) {
	e.PrevHash = prevHash
	e.RowHash = ChainHash(prevHash, *e)
}

// VerifyResult is what a verification of one run of rows found.
type VerifyResult struct {
	OK      bool  `json:"ok"`
	Checked int   `json:"checked"`
	FromSeq int64 `json:"from_seq,omitempty"`
	ToSeq   int64 `json:"to_seq,omitempty"`
	// AnchorSeq is the seq the first checked row chains from: 0 for the
	// genesis anchor, otherwise the row before the first one checked.
	AnchorSeq int64 `json:"anchor_seq"`
	// StartsMidChain reports that the first row is not the line's first, so
	// its prev_hash is taken on trust as the anchor.
	StartsMidChain bool `json:"starts_mid_chain"`
	// FirstBadSeq is the first seq at which the run stops verifying; zero when
	// OK. For a gap it is the first missing seq.
	FirstBadSeq int64 `json:"first_bad_seq,omitempty"`
	// Problem says in the console's language what is wrong at FirstBadSeq.
	Problem string `json:"problem,omitempty"`
	// Note qualifies an OK result, e.g. that earlier rows were pruned.
	Note string `json:"note,omitempty"`
	// HeadSeq and HeadHash are the last row checked, as recomputed.
	HeadSeq  int64  `json:"head_seq,omitempty"`
	HeadHash string `json:"head_hash,omitempty"`
}

// NotePruned is the note on a run whose first row is not the line's first and
// before which nothing remains: the earlier rows were removed by retention.
const NotePruned = "更早的记录已按保留期清理，本次以现存第一条记录所指向的上一环哈希为起点校验"

// NoteSegment is the note on a run that starts in the middle of a chain whose
// earlier rows still exist but were not part of this check.
const NoteSegment = "本次只校验了这一段，以段首记录所指向的上一环哈希为起点；更早的记录可用整段验链另行校验"

// Verify checks a run of rows of one product line, ordered by seq: that seq
// has no gaps, that each prev_hash is the previous row's row_hash, and that
// each row_hash recomputes. A run that starts at seq 1 is anchored at the
// genesis hash; one that starts later takes its first prev_hash as the anchor
// and says so (NotePruned; callers that know earlier rows exist replace it
// with NoteSegment).
//
// The row hash is checked before the link, so a field changed in row k is
// reported at k rather than at k+1.
func Verify(rows []AuditEntry) VerifyResult {
	res := VerifyResult{OK: true}
	if len(rows) == 0 {
		return res
	}
	first := rows[0]
	res.FromSeq = first.Seq
	res.ToSeq = rows[len(rows)-1].Seq

	expectedPrev := first.PrevHash
	if first.Seq <= 1 {
		expectedPrev = GenesisHash(first.ProductLineID)
	} else {
		res.StartsMidChain = true
		res.AnchorSeq = first.Seq - 1
		res.Note = NotePruned
	}

	fail := func(seq int64, problem string) VerifyResult {
		res.OK = false
		res.FirstBadSeq = seq
		res.Problem = problem
		res.Note = ""
		return res
	}

	for i, row := range rows {
		if row.ProductLineID != first.ProductLineID {
			return fail(row.Seq, "这一条记录不属于本条链所在的租户")
		}
		if i == 0 && first.Seq < 1 {
			return fail(row.Seq, "序号必须从 1 开始")
		}
		if i > 0 {
			want := rows[i-1].Seq + 1
			if row.Seq != want {
				if row.Seq < want {
					return fail(row.Seq, fmt.Sprintf("序号重复或倒序：第 %d 条之后出现了第 %d 条", rows[i-1].Seq, row.Seq))
				}
				return fail(want, fmt.Sprintf("序号断档：第 %d 条之后直接是第 %d 条，中间的记录缺失", rows[i-1].Seq, row.Seq))
			}
		}
		if ChainHash(row.PrevHash, row) != row.RowHash {
			return fail(row.Seq, fmt.Sprintf("第 %d 条记录的内容与其哈希不符，记录写入后被改动过", row.Seq))
		}
		if row.PrevHash != expectedPrev {
			if i == 0 {
				return fail(row.Seq, "第 1 条记录没有接在本租户的链首上")
			}
			return fail(row.Seq, fmt.Sprintf("第 %d 条记录没有接在第 %d 条之后，链在这里被改动过", row.Seq, rows[i-1].Seq))
		}
		expectedPrev = row.RowHash
		res.Checked++
	}
	last := rows[len(rows)-1]
	res.HeadSeq = last.Seq
	res.HeadHash = last.RowHash
	return res
}

// headDigestPrefix opens every head digest line; the version lets a later
// format coexist with digests customers already saved.
const headDigestPrefix = "unica-answer-audit-head/v1"

// HeadDigest renders a chain head as one printable line a customer can save
// outside this system. It is the external anchor: a chain rewritten after the
// line was saved can still verify internally, but its row at that seq will no
// longer carry this hash.
func HeadDigest(productLineID string, seq int64, rowHash string) string {
	return fmt.Sprintf("%s product_line=%s seq=%d row_hash=%s", headDigestPrefix, productLineID, seq, rowHash)
}

// HeadRef is a parsed head digest.
type HeadRef struct {
	ProductLineID string
	Seq           int64
	RowHash       string
}

// ParseHeadDigest reads a line written by HeadDigest.
func ParseHeadDigest(line string) (HeadRef, error) {
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) != 4 || fields[0] != headDigestPrefix {
		return HeadRef{}, fmt.Errorf("不是链头摘要：应以 %s 开头并包含 product_line、seq、row_hash 三项", headDigestPrefix)
	}
	var ref HeadRef
	seen := map[string]bool{}
	for _, f := range fields[1:] {
		k, v, ok := strings.Cut(f, "=")
		if !ok || v == "" || seen[k] {
			return HeadRef{}, fmt.Errorf("链头摘要中的 %q 无法识别", f)
		}
		seen[k] = true
		switch k {
		case "product_line":
			ref.ProductLineID = v
		case "seq":
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n < 0 {
				return HeadRef{}, fmt.Errorf("链头摘要中的序号 %q 无效", v)
			}
			ref.Seq = n
		case "row_hash":
			if len(v) != 64 {
				return HeadRef{}, fmt.Errorf("链头摘要中的哈希长度不对")
			}
			ref.RowHash = v
		default:
			return HeadRef{}, fmt.Errorf("链头摘要中的 %q 无法识别", f)
		}
	}
	if !seen["product_line"] || !seen["seq"] || !seen["row_hash"] {
		return HeadRef{}, fmt.Errorf("链头摘要不完整")
	}
	return ref, nil
}

// CompareHead checks a saved head digest against a run of rows that has
// already passed Verify and contains the digest's seq. A mismatch means the
// chain was rewritten after the digest was saved, even though it still
// verifies on its own.
func CompareHead(ref HeadRef, rows []AuditEntry) error {
	if ref.Seq == 0 {
		if len(rows) > 0 && ref.RowHash != GenesisHash(rows[0].ProductLineID) {
			return fmt.Errorf("链头摘要记录的是空链，但哈希与本租户的链首不符")
		}
		return nil
	}
	for _, row := range rows {
		if row.Seq != ref.Seq {
			continue
		}
		if row.ProductLineID != ref.ProductLineID {
			return fmt.Errorf("链头摘要属于另一个租户")
		}
		if row.RowHash != ref.RowHash {
			return fmt.Errorf("第 %d 条记录的哈希与留存的链头摘要不一致：这条记录或它之前的记录在摘要留存之后被改动过", ref.Seq)
		}
		return nil
	}
	return fmt.Errorf("校验范围里没有第 %d 条记录，无法与留存的链头摘要比对", ref.Seq)
}
