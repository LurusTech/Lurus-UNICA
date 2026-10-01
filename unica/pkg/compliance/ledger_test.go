package compliance

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const testLine = "6f1c2a3e-0000-4000-8000-000000000001"

func strp(s string) *string   { return &s }
func f64p(f float64) *float64 { return &f }
func intp(i int) *int         { return &i }
func cloneRows(rows []AuditEntry) []AuditEntry {
	out := make([]AuditEntry, len(rows))
	copy(out, rows)
	return out
}

// buildChain seals n rows the way the store does: normalise, number, stamp,
// chain onto the previous head.
func buildChain(t *testing.T, line string, n int) []AuditEntry {
	t.Helper()
	base := time.Date(2026, 9, 30, 8, 0, 0, 123456000, time.UTC)
	head := GenesisHash(line)
	rows := make([]AuditEntry, 0, n)
	for i := 1; i <= n; i++ {
		e := AuditEntry{
			ProductLineID:   line,
			ConversationID:  "c0ffee00-0000-4000-8000-00000000000" + string(rune('0'+i%10)),
			CorrelationID:   "corr-" + string(rune('a'+i%26)),
			Kind:            KindJudged,
			Decision:        "send",
			Reason:          "confidence_ok",
			ReasonClass:     ReasonClassNone,
			ConfidenceScore: f64p(0.8125),
			OntologyVersion: intp(3),
			FactsInjected:   true,
			ValidationMode:  "shadow",
			Claims:          json.RawMessage(`[{"property":"return_window_days","value":"7"}]`),
			Violations:      json.RawMessage(`[]`),
			Commitments:     json.RawMessage(`[{"kind":"unbacked_commitment","got":"全额","message":"无出处"}]`),
			Sources:         json.RawMessage(`[{"document_name":"退货政策","score":0.91,"content_sha256":"ab"}]`),
			ConfigSHA256:    strings.Repeat("c", 64),
			CustomerMessage: "可以退货吗",
			DeliveredAnswer: strp("支持7天无理由退货。"),
		}
		if i%3 == 0 {
			e.Kind = KindNoAnswer
			e.Decision = "handoff"
			e.Reason = "ai_unavailable"
			e.ReasonClass = "other"
			e.ConfidenceScore = nil
			e.OntologyVersion = nil
			e.DeliveredAnswer = nil
			e.WithheldAnswer = strp("被拦下的草稿")
		}
		e.normalise()
		e.Seq = int64(i)
		e.CreatedAt = base.Add(time.Duration(i) * time.Second)
		e.Seal(head)
		head = e.RowHash
		rows = append(rows, e)
	}
	return rows
}

func TestVerify_IntactChainFromGenesis(t *testing.T) {
	rows := buildChain(t, testLine, 12)
	res := Verify(rows)
	if !res.OK || res.Checked != 12 || res.StartsMidChain || res.AnchorSeq != 0 {
		t.Fatalf("intact chain must verify from genesis: %+v", res)
	}
	if res.HeadSeq != 12 || res.HeadHash != rows[11].RowHash {
		t.Errorf("head = %d/%s, want 12/%s", res.HeadSeq, res.HeadHash, rows[11].RowHash)
	}
	if res.Note != "" || res.Problem != "" {
		t.Errorf("an intact chain from genesis needs no note: %+v", res)
	}
}

func TestVerify_EmptyIsOK(t *testing.T) {
	if res := Verify(nil); !res.OK || res.Checked != 0 {
		t.Errorf("empty run = %+v", res)
	}
}

// Every column the hash covers, changed in one row, must be reported at that
// row and not later.
func TestVerify_AnyFieldChangeIsFoundAtItsRow(t *testing.T) {
	mutations := map[string]func(e *AuditEntry){
		"delivered_answer one char": func(e *AuditEntry) {
			r := []rune(*e.DeliveredAnswer)
			r[2] = '9'
			e.DeliveredAnswer = strp(string(r))
		},
		"delivered_answer removed": func(e *AuditEntry) { e.DeliveredAnswer = nil },
		"withheld_answer added":    func(e *AuditEntry) { e.WithheldAnswer = strp("x") },
		"customer_message":         func(e *AuditEntry) { e.CustomerMessage += " " },
		"conversation_id":          func(e *AuditEntry) { e.ConversationID = "c0ffee00-0000-4000-8000-0000000000ff" },
		"correlation_id":           func(e *AuditEntry) { e.CorrelationID = "" },
		"kind":                     func(e *AuditEntry) { e.Kind = KindPreAI },
		"decision":                 func(e *AuditEntry) { e.Decision = "handoff" },
		"reason":                   func(e *AuditEntry) { e.Reason = "low_confidence" },
		"reason_class":             func(e *AuditEntry) { e.ReasonClass = "forced" },
		"confidence_score":         func(e *AuditEntry) { e.ConfidenceScore = f64p(0.8126) },
		"confidence_score null":    func(e *AuditEntry) { e.ConfidenceScore = nil },
		"ontology_version":         func(e *AuditEntry) { e.OntologyVersion = intp(4) },
		"facts_injected":           func(e *AuditEntry) { e.FactsInjected = !e.FactsInjected },
		"validation_mode":          func(e *AuditEntry) { e.ValidationMode = "enforce" },
		"breaker_open":             func(e *AuditEntry) { e.BreakerOpen = !e.BreakerOpen },
		"enforced":                 func(e *AuditEntry) { e.Enforced = !e.Enforced },
		"claims":                   func(e *AuditEntry) { e.Claims = json.RawMessage(`[{"property":"return_window_days","value":"15"}]`) },
		"violations":               func(e *AuditEntry) { e.Violations = json.RawMessage(`[{"kind":"range"}]`) },
		"commitments emptied":      func(e *AuditEntry) { e.Commitments = json.RawMessage(`[]`) },
		"sources":                  func(e *AuditEntry) { e.Sources = json.RawMessage(`[{"document_name":"退货政策","score":0.92}]`) },
		"config_sha256":            func(e *AuditEntry) { e.ConfigSHA256 = strings.Repeat("d", 64) },
		"created_at":               func(e *AuditEntry) { e.CreatedAt = e.CreatedAt.Add(time.Microsecond) },
		"seq renumbered":           func(e *AuditEntry) { e.Seq = 100 },
		"row_hash":                 func(e *AuditEntry) { e.RowHash = strings.Repeat("0", 64) },
		"prev_hash":                func(e *AuditEntry) { e.PrevHash = strings.Repeat("0", 64) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			rows := buildChain(t, testLine, 8)
			const target = 5
			// Row 5 is a judged row (5 % 3 != 0), so every field is populated.
			mutate(&rows[target-1])
			res := Verify(rows)
			if res.OK {
				t.Fatal("a changed row must not verify")
			}
			if res.FirstBadSeq != target {
				t.Errorf("FirstBadSeq = %d, want %d (%s)", res.FirstBadSeq, target, res.Problem)
			}
			if res.Problem == "" {
				t.Error("a failed check must say what is wrong")
			}
		})
	}
}

func TestVerify_DeletedMiddleRowIsAGap(t *testing.T) {
	rows := buildChain(t, testLine, 10)
	cut := append(cloneRows(rows[:4]), rows[5:]...) // seq 5 removed
	res := Verify(cut)
	if res.OK {
		t.Fatal("a missing row must not verify")
	}
	if res.FirstBadSeq != 5 || !strings.Contains(res.Problem, "断档") {
		t.Errorf("gap reported as %d %q, want seq 5 and a gap", res.FirstBadSeq, res.Problem)
	}
}

func TestVerify_PrunedHeadAnchorsOnFirstSurvivingRow(t *testing.T) {
	rows := buildChain(t, testLine, 10)
	res := Verify(rows[4:]) // seq 1..4 dropped by retention
	if !res.OK || res.Checked != 6 {
		t.Fatalf("a chain with its head pruned must still verify: %+v", res)
	}
	if !res.StartsMidChain || res.AnchorSeq != 4 || res.FromSeq != 5 {
		t.Errorf("anchor = %+v, want mid-chain from seq 5 anchored at 4", res)
	}
	if res.Note != NotePruned || !strings.Contains(res.Note, "已按保留期清理") {
		t.Errorf("the pruning must be stated, got note %q", res.Note)
	}
}

// A row that claims to be first must chain from this line's genesis, so a
// chain cannot be copied under another tenant's id and still verify.
func TestVerify_GenesisIsBoundToTheLine(t *testing.T) {
	rows := buildChain(t, testLine, 3)
	moved := cloneRows(rows)
	for i := range moved {
		moved[i].ProductLineID = "6f1c2a3e-0000-4000-8000-000000000002"
	}
	if res := Verify(moved); res.OK || res.FirstBadSeq != 1 {
		t.Errorf("a chain moved to another line must fail at seq 1, got %+v", res)
	}
	if GenesisHash(testLine) == GenesisHash("6f1c2a3e-0000-4000-8000-000000000002") {
		t.Error("genesis hashes of two lines must differ")
	}
}

// Someone with database access can rewrite a row and recompute every hash
// after it. The chain then verifies on its own; only the head digest saved
// outside the system tells the difference.
func TestHeadDigest_DetectsARecomputedChain(t *testing.T) {
	rows := buildChain(t, testLine, 6)
	saved := HeadDigest(testLine, 6, rows[5].RowHash)

	ref, err := ParseHeadDigest(saved)
	if err != nil {
		t.Fatalf("parse saved digest: %v", err)
	}
	if err := CompareHead(ref, rows); err != nil {
		t.Fatalf("the untouched chain must match its own digest: %v", err)
	}

	forged := cloneRows(rows)
	forged[2].DeliveredAnswer = strp("支持30天无理由退货。")
	prev := forged[1].RowHash
	for i := 2; i < len(forged); i++ {
		forged[i].Seal(prev)
		prev = forged[i].RowHash
	}
	if res := Verify(forged); !res.OK {
		t.Fatalf("the recomputed chain is internally consistent by construction: %+v", res)
	}
	if err := CompareHead(ref, forged); err == nil {
		t.Fatal("the saved digest must expose a recomputed chain")
	}

	// A digest saved earlier still anchors a chain that has grown since.
	longer := buildChain(t, testLine, 9)
	if err := CompareHead(ref, longer); err != nil {
		t.Errorf("a grown chain must still match an older digest: %v", err)
	}
}

func TestHeadDigest_RoundTripAndRejects(t *testing.T) {
	hash := strings.Repeat("a", 64)
	line := HeadDigest(testLine, 42, hash)
	if strings.ContainsAny(line, "\n\r") {
		t.Fatalf("the digest must be one line: %q", line)
	}
	ref, err := ParseHeadDigest("  " + line + "\n")
	if err != nil || ref.ProductLineID != testLine || ref.Seq != 42 || ref.RowHash != hash {
		t.Fatalf("round trip = %+v, %v", ref, err)
	}
	for _, bad := range []string{
		"",
		"hello world",
		strings.Replace(line, "seq=42", "seq=x", 1),
		strings.Replace(line, hash, "abc", 1),
		strings.Replace(line, "product_line=", "tenant=", 1),
		line + " extra=1",
	} {
		if _, err := ParseHeadDigest(bad); err == nil {
			t.Errorf("ParseHeadDigest(%q) must fail", bad)
		}
	}
}

// JSONB hands JSON back with its keys reordered and whitespace dropped; the
// hash must not notice.
func TestCanonical_StableAcrossJSONBReformatting(t *testing.T) {
	a := AuditEntry{
		ProductLineID: testLine,
		Seq:           1,
		Sources:       json.RawMessage(`[ {"score": 0.5, "document_name": "甲", "content_sha256": "ff"} ]`),
		Claims:        json.RawMessage(`null`),
		CreatedAt:     time.Date(2026, 10, 1, 0, 0, 0, 1000, time.FixedZone("CST", 8*3600)),
	}
	b := a
	b.Sources = json.RawMessage(`[{"content_sha256":"ff","document_name":"甲","score":0.5}]`)
	b.Claims = json.RawMessage(`[]`)
	b.CreatedAt = a.CreatedAt.UTC()
	if string(a.Canonical()) != string(b.Canonical()) {
		t.Errorf("canonical forms differ:\n%s\n%s", a.Canonical(), b.Canonical())
	}
}

func TestNormalise_KeepsRowsStorable(t *testing.T) {
	e := AuditEntry{
		Reason:          strings.Repeat("r", 80),
		CustomerMessage: "a\x00b\xff",
		Claims:          json.RawMessage(`[{"value":"x\u0000y"}]`),
	}
	e.normalise()
	if len([]rune(e.Reason)) != maxReasonRunes {
		t.Errorf("reason not cut to the column width: %d", len([]rune(e.Reason)))
	}
	if strings.Contains(e.CustomerMessage, "\x00") {
		t.Errorf("NUL survived: %q", e.CustomerMessage)
	}
	if strings.Contains(string(e.Claims), `\u0000`) {
		t.Errorf("NUL survived in JSON: %s", e.Claims)
	}
	if e.ValidationMode != "off" {
		t.Errorf("an empty validation mode is stored as off, got %q", e.ValidationMode)
	}
	if string(e.Violations) != "[]" {
		t.Errorf("an empty JSON column is stored as [], got %s", e.Violations)
	}
}

func TestLoadAuditConfig_DefaultsOff(t *testing.T) {
	cases := map[string]bool{
		``:                                   false,
		`{}`:                                 false,
		`not json`:                           false,
		`{"audit_ledger":null}`:              false,
		`{"audit_ledger":{"enabled":"yes"}}`: false,
		`{"audit_ledger":{}}`:                false,
		`{"audit_ledger":{"enabled":false}}`: false,
		`{"audit_ledger":{"enabled":true}}`:  true,
	}
	for raw, want := range cases {
		if got := LoadAuditConfig(json.RawMessage(raw)).Enabled; got != want {
			t.Errorf("LoadAuditConfig(%s).Enabled = %v, want %v", raw, got, want)
		}
	}
}

// The wording promises discoverability, never prevention.
func TestWording_NoPreventionClaims(t *testing.T) {
	for _, s := range []string{NotePruned, NoteSegment} {
		for _, banned := range []string{"防篡改", "国标"} {
			if strings.Contains(s, banned) {
				t.Errorf("%q must not say %q", s, banned)
			}
		}
	}
}
