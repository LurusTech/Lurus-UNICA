package replay

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kefu/unica/pkg/domain"
)

// mineTestOntologyYAML is a small, self-contained ontology so mining tests
// do not depend on the shared FreshMart fixture's own vocabulary lining up
// with the phrases these tests need. warranty_months is declared but never
// asserted a value, on purpose: it is what TestMine_MissingFactHasNoValue
// expects mining to notice.
const mineTestOntologyYAML = `
product_line: TestLine
version: 1
properties:
  return_precondition:
    label: 退款前置条件
    range:
      type: enum
      values: [quality_defect, photo_evidence]
      labels: {quality_defect: 质量问题, photo_evidence: 拍照取证}
    min_cardinality: 1
  refund_days:
    label: 退款到账天数
    range: {type: integer, values: ["3"]}
    functional: true
  warranty_months:
    label: 质保期
    range: {type: integer, values: ["12"]}
    functional: true
assertions:
  - values:
      return_precondition: [quality_defect, photo_evidence]
      refund_days: 3
denies:
  - term: 无理由退货
`

func mineTestOntology(t *testing.T) *domain.Ontology {
	t.Helper()
	o, err := domain.ParseYAML([]byte(mineTestOntologyYAML))
	if err != nil {
		t.Fatalf("parse test ontology: %v", err)
	}
	return o
}

// The following conversation IDs are fixed rather than generated, so the
// train/holdout split they land on (InHoldout, split.go) never changes
// between runs: conv-001 is on the holdout side, every other id here is on
// the training side. A helper test (not committed) walked conv-000..conv-030
// to find them.
const (
	holdoutConvID = "conv-001"

	cluster1ConvA = "conv-000"
	cluster1ConvB = "conv-002"
	cluster1ConvC = "conv-003"

	forcedConvA = "conv-006"
	forcedConvB = "conv-007"
	forcedConvC = "conv-008"

	ambiguousConvA = "conv-010"
	ambiguousConvB = "conv-012"
	ambiguousConvC = "conv-013"

	actionConvA = "conv-015"
	actionConvB = "conv-017"
	actionConvC = "conv-018"

	longTailConvA = "conv-021"
	longTailConvB = "conv-022"

	deliveredConvID = "conv-024"
)

func handoffCase(convID, query, reason, reference string) Case {
	return Case{
		ID:             convID + "#1",
		ConversationID: convID,
		ProductLine:    "TestLine",
		Query:          query,
		Logged:         Outcome{Decision: "handoff", Reason: reason},
		Reference:      reference,
	}
}

// mineTestCases builds the fixed scenario every test in this file reads
// from: three clusters (a minable alias, a forced-only cluster, and an
// ambiguous-evidence cluster), an action-type group, a long tail and one
// delivered turn that never escalated at all.
func mineTestCases() []Case {
	var cases []Case

	// Cluster 1: reducible, unanimous evidence for return_precondition's
	// quality_defect value — should mine an alias.
	for _, id := range []string{cluster1ConvA, cluster1ConvB, cluster1ConvC} {
		q := map[string]string{
			cluster1ConvA: "东西坏了能退吗",
			cluster1ConvB: "东西坏了能退不",
			cluster1ConvC: "东西坏了能退么",
		}[id]
		cases = append(cases, handoffCase(id, q, "low_confidence", "已核实为质量问题，可以退款"))
	}

	// Cluster 2: forced reason throughout. Its agent replies also surface a
	// gap (质保期 is declared but never asserted), so this cluster doubles as
	// the missing-fact fixture — proving that ticket does not depend on the
	// cluster being reducible.
	for _, id := range []string{forcedConvA, forcedConvB, forcedConvC} {
		q := map[string]string{
			forcedConvA: "这个问题必须转人工处理",
			forcedConvB: "这个问题必须转人工解决",
			forcedConvC: "这个问题必须转人工来处理",
		}[id]
		cases = append(cases, handoffCase(id, q, "funds_at_risk", "已升级，质保期内可以免费维修"))
	}

	// Cluster 3: reducible, but the cluster's own agent replies corroborate
	// two different declared targets (质量问题 and 拍照取证) — ambiguous, so
	// mining must discard every candidate phrase rather than guess.
	refs := map[string]string{
		ambiguousConvA: "已核实为质量问题，可以退款",
		ambiguousConvB: "需要拍照取证才能受理",
		ambiguousConvC: "已核实为质量问题，可以退款",
	}
	for _, id := range []string{ambiguousConvA, ambiguousConvB, ambiguousConvC} {
		q := map[string]string{
			ambiguousConvA: "东西不想要了能退不",
			ambiguousConvB: "东西不想要了能退吗",
			ambiguousConvC: "东西不想要了能退么",
		}[id]
		cases = append(cases, handoffCase(id, q, "low_confidence", refs[id]))
	}

	// Action-type: asks the AI to look something up, not to answer policy.
	cases = append(cases,
		handoffCase(actionConvA, "帮我查一下订单号的物流到哪了", "low_confidence", ""),
		handoffCase(actionConvB, "查一下退款进度到哪了", "low_confidence", ""),
		handoffCase(actionConvC, "查下我的运单号什么时候到", "low_confidence", ""),
	)

	// Long tail: escalations that share nothing with anything else.
	cases = append(cases,
		handoffCase(longTailConvA, "退款怎么还没到账", "low_confidence", ""),
		handoffCase(longTailConvB, "怎么联系人工客服", "low_confidence", ""),
	)

	// Delivered, never escalated: must not enter TotalHandoffs at all.
	cases = append(cases, Case{
		ID: deliveredConvID + "#1", ConversationID: deliveredConvID, ProductLine: "TestLine",
		Query: "退款到账要几天", Logged: Outcome{Decision: "send", Reason: "confidence_ok"},
	})

	return cases
}

func TestMine_HoldoutCaseIsRejected(t *testing.T) {
	o := mineTestOntology(t)
	cases := []Case{handoffCase(holdoutConvID, "随便问问", "low_confidence", "")}
	if !InHoldout(holdoutConvID) {
		t.Fatalf("fixture assumption broken: %s must be on the holdout side", holdoutConvID)
	}
	_, err := Mine(cases, o, DefaultMineOptions())
	if err == nil {
		t.Fatal("expected an error when a holdout case is mixed into the training input")
	}
	if !strings.Contains(err.Error(), holdoutConvID) {
		t.Errorf("error should name the offending conversation, got: %v", err)
	}
}

func TestMine_HoldoutRejectionIsPhysicalNotPartial(t *testing.T) {
	// One holdout case among many training ones must still fail the whole
	// call — a caller that mixed splits gets no report at all, not a report
	// silently missing the leaked case.
	o := mineTestOntology(t)
	cases := append(mineTestCases(), handoffCase(holdoutConvID, "随便问问", "low_confidence", ""))
	report, err := Mine(cases, o, DefaultMineOptions())
	if err == nil {
		t.Fatal("expected an error")
	}
	if report != nil {
		t.Errorf("expected no report on a holdout violation, got %+v", report)
	}
}

func TestMine_TotalHandoffsExcludesDelivered(t *testing.T) {
	o := mineTestOntology(t)
	report, err := Mine(mineTestCases(), o, DefaultMineOptions())
	if err != nil {
		t.Fatal(err)
	}
	const wantEscalated = 3 + 3 + 3 + 3 + 2 // cluster1 + forced + ambiguous + action + long tail
	if report.TotalHandoffs != wantEscalated {
		t.Errorf("total handoffs = %d, want %d (the delivered turn must not count)", report.TotalHandoffs, wantEscalated)
	}
}

func TestMine_ActionTypeIsReportedNotClustered(t *testing.T) {
	o := mineTestOntology(t)
	report, err := Mine(mineTestCases(), o, DefaultMineOptions())
	if err != nil {
		t.Fatal(err)
	}
	if report.ActionCount != 3 {
		t.Errorf("action count = %d, want 3", report.ActionCount)
	}
	for _, c := range report.Clusters {
		for _, q := range c.Representative {
			if strings.Contains(q, "物流") || strings.Contains(q, "查一下") {
				t.Errorf("action-type query %q leaked into cluster %d", q, c.ID)
			}
		}
	}
}

func TestMine_MinClusterSizeIsThree(t *testing.T) {
	o := mineTestOntology(t)
	report, err := Mine(mineTestCases(), o, DefaultMineOptions())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range report.Clusters {
		if c.Size < MinClusterSize {
			t.Errorf("cluster %d has size %d, below MinClusterSize %d", c.ID, c.Size, MinClusterSize)
		}
	}
	// The two long-tail turns share nothing with each other or with anything
	// else, so together they must land in LongTailCount rather than forming
	// a reported cluster of size 2.
	if report.LongTailCount != 2 {
		t.Errorf("long tail count = %d, want 2", report.LongTailCount)
	}
	wantShare := 2.0 / float64(report.TotalHandoffs)
	if report.LongTailShare != wantShare {
		t.Errorf("long tail share = %v, want %v", report.LongTailShare, wantShare)
	}
}

func TestMine_ForcedClusterNeverProducesAnAlias(t *testing.T) {
	o := mineTestOntology(t)
	report, err := Mine(mineTestCases(), o, DefaultMineOptions())
	if err != nil {
		t.Fatal(err)
	}
	var forcedCluster *ClusterReport
	for i := range report.Clusters {
		if report.Clusters[i].Forced {
			forcedCluster = &report.Clusters[i]
		}
	}
	if forcedCluster == nil {
		t.Fatal("expected the forced-reason cases to form a reported cluster")
	}
	if forcedCluster.Size != 3 {
		t.Errorf("forced cluster size = %d, want 3", forcedCluster.Size)
	}
	for _, a := range report.AliasCandidates {
		if a.ClusterID == forcedCluster.ID {
			t.Errorf("forced cluster %d produced an alias candidate: %+v", forcedCluster.ID, a)
		}
	}
	for _, h := range report.DimensionHints {
		if h.ClusterID == forcedCluster.ID {
			t.Errorf("forced cluster %d produced a dimension hint %+v; forced clusters are report-only", forcedCluster.ID, h)
		}
	}
}

func TestMine_AliasCandidateFromUnambiguousEvidence(t *testing.T) {
	o := mineTestOntology(t)
	report, err := Mine(mineTestCases(), o, DefaultMineOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.AliasCandidates) != 1 {
		t.Fatalf("alias candidates = %d, want exactly 1: %+v", len(report.AliasCandidates), report.AliasCandidates)
	}
	cand := report.AliasCandidates[0]
	wantRef := domain.ValueAlias("return_precondition", "quality_defect")
	if cand.Ref != wantRef {
		t.Errorf("alias ref = %s, want %s", cand.Ref, wantRef)
	}
	if !strings.Contains("东西坏了能退吗", cand.Phrase) &&
		!strings.Contains("东西坏了能退不", cand.Phrase) &&
		!strings.Contains("东西坏了能退么", cand.Phrase) {
		t.Errorf("phrase %q was not drawn from cluster 1's own messages", cand.Phrase)
	}
	if cand.Occurrences < MinClusterSize {
		t.Errorf("occurrences = %d, below MinClusterSize", cand.Occurrences)
	}
	if len(cand.Evidence) == 0 {
		t.Error("alias candidate carries no evidence")
	}

	// The candidate must actually apply and classify as tier A against the
	// base ontology — mining's own claim is only as good as this check.
	applied, err := domain.ApplyAlias(o, cand.Ref, cand.Phrase)
	if err != nil {
		t.Fatalf("ApplyAlias(%s, %q) failed on mining's own candidate: %v", cand.Ref, cand.Phrase, err)
	}
	if tier := domain.ClassifyDiff(o, applied); tier != domain.TierAlias {
		t.Errorf("classified as %q, want tier A", tier)
	}
}

func TestMine_AmbiguousEvidenceIsDiscarded(t *testing.T) {
	o := mineTestOntology(t)
	report, err := Mine(mineTestCases(), o, DefaultMineOptions())
	if err != nil {
		t.Fatal(err)
	}
	var ambiguousCluster *ClusterReport
	for i := range report.Clusters {
		for _, q := range report.Clusters[i].Representative {
			if strings.Contains(q, "不想要了") {
				ambiguousCluster = &report.Clusters[i]
			}
		}
	}
	if ambiguousCluster == nil {
		t.Fatal("expected the ambiguous-evidence cases to form a reported cluster")
	}
	for _, a := range report.AliasCandidates {
		if a.ClusterID == ambiguousCluster.ID {
			t.Errorf("ambiguous cluster %d produced an alias candidate anyway: %+v", ambiguousCluster.ID, a)
		}
	}
	found := false
	for _, h := range report.DimensionHints {
		if h.ClusterID == ambiguousCluster.ID {
			found = true
			if !strings.Contains(h.Reason, "歧义") {
				t.Errorf("dimension hint reason = %q, want it to name the ambiguity", h.Reason)
			}
		}
	}
	if !found {
		t.Errorf("expected a dimension hint for the ambiguous cluster %d", ambiguousCluster.ID)
	}
}

// TestMine_AllFrequentPhrasesAlreadyCanonicalIsReportedAsSuch exercises
// mineAliasCandidate directly (white-box: same package) for the one clause
// Mine's end-to-end fixture cannot reach through ordinary customer language —
// a cluster whose only frequent phrase is already the ontology's own wording.
// That must read to a reviewer as "nothing new here", not as "no evidence was
// found", which was the bug this test guards against: the two reasons used to
// share one message.
func TestMine_AllFrequentPhrasesAlreadyCanonicalIsReportedAsSuch(t *testing.T) {
	const yaml = `
product_line: TestLine
version: 1
properties:
  color:
    label: 颜色
    range:
      type: enum
      values: [red]
      labels: {red: 红色}
    min_cardinality: 1
assertions:
  - values:
      color: [red]
aliases:
  value:color.red: [变红]
`
	o, err := domain.ParseYAML([]byte(yaml))
	if err != nil {
		t.Fatalf("parse test ontology: %v", err)
	}

	// Every customer message is exactly the existing 2-rune alias "变红", so
	// frequentPhrases has exactly one candidate (min/max phrase length both
	// bottom out at the string's own length) and it is already declared.
	var cluster []Case
	caseByID := map[string]Case{}
	for _, id := range []string{"conv-100", "conv-101", "conv-102"} {
		c := handoffCase(id, "变红", "low_confidence", "")
		cluster = append(cluster, c)
		caseByID[c.ID] = c
	}

	cand, whyNot := mineAliasCandidate(o, cluster, caseByID)
	if cand != nil {
		t.Fatalf("expected no candidate, got %+v", cand)
	}
	if !strings.Contains(whyNot, "已经是规范说法或已有别名") {
		t.Errorf("reason = %q, want it to say every frequent phrase was already declared/aliased, not that evidence was missing", whyNot)
	}
	if strings.Contains(whyNot, "没有坐席参照回复佐证") {
		t.Errorf("reason = %q, must not be conflated with the no-evidence case", whyNot)
	}
}

func TestMine_MissingFactHasNoValueAndNamesADeclaredProperty(t *testing.T) {
	o := mineTestOntology(t)
	report, err := Mine(mineTestCases(), o, DefaultMineOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.MissingFacts) != 1 {
		t.Fatalf("missing facts = %d, want exactly 1: %+v", len(report.MissingFacts), report.MissingFacts)
	}
	mf := report.MissingFacts[0]
	if mf.Property != "warranty_months" {
		t.Errorf("missing fact property = %q, want warranty_months", mf.Property)
	}
	if !mf.Declared {
		t.Error("warranty_months is declared in the test ontology; Declared should be true")
	}
	if len(mf.Evidence) == 0 {
		t.Error("missing fact carries no evidence")
	}
	// MissingFact has no field a value could occupy at all — this is
	// enforced by the type (proposal.go), not just by mining's own
	// discipline, so a marshalled ticket can never carry one either.
	data, err := json.Marshal(mf)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"value"`) {
		t.Errorf("missing fact JSON must never carry a value field: %s", data)
	}
}

func TestMine_Deterministic(t *testing.T) {
	o := mineTestOntology(t)
	cases := mineTestCases()

	report1, err := Mine(cases, o, DefaultMineOptions())
	if err != nil {
		t.Fatal(err)
	}

	// Shuffle the input order; the output must not depend on it.
	shuffled := make([]Case, len(cases))
	n := len(cases)
	for i, c := range cases {
		shuffled[n-1-i] = c
	}
	report2, err := Mine(shuffled, o, DefaultMineOptions())
	if err != nil {
		t.Fatal(err)
	}

	j1, err := json.Marshal(report1)
	if err != nil {
		t.Fatal(err)
	}
	j2, err := json.Marshal(report2)
	if err != nil {
		t.Fatal(err)
	}
	if string(j1) != string(j2) {
		t.Errorf("mining is not deterministic under input reordering:\n%s\nvs\n%s", j1, j2)
	}

	// And a second run on the identical, unshuffled input must match byte
	// for byte too.
	report3, err := Mine(cases, o, DefaultMineOptions())
	if err != nil {
		t.Fatal(err)
	}
	j3, err := json.Marshal(report3)
	if err != nil {
		t.Fatal(err)
	}
	if string(j1) != string(j3) {
		t.Errorf("mining is not deterministic across repeated runs:\n%s\nvs\n%s", j1, j3)
	}
}

func TestMine_MarkdownDoesNotPanicAndMentionsForced(t *testing.T) {
	o := mineTestOntology(t)
	report, err := Mine(mineTestCases(), o, DefaultMineOptions())
	if err != nil {
		t.Fatal(err)
	}
	md := report.Markdown()
	if md == "" {
		t.Fatal("empty markdown")
	}
	if !strings.Contains(md, "只报告") {
		t.Error("markdown should call out that forced clusters are report-only")
	}
}

// TestMine_UndeclaredPropertyBecomesAnUndeclaredTicket covers the second
// missing-fact signal: the live validator logged the AI claiming a property
// the ontology never declared. The ticket names the model's own word, says it
// is undeclared, and carries no value; a property declared since is skipped.
func TestMine_UndeclaredPropertyBecomesAnUndeclaredTicket(t *testing.T) {
	o := mineTestOntology(t)
	var cases []Case
	for _, id := range []string{cluster1ConvA, cluster1ConvB, cluster1ConvC} {
		c := handoffCase(id, "积分能抵多少钱", "claim_conflict", "")
		c.LoggedViolations = []domain.Violation{
			{Kind: domain.ViolationUndeclared, Property: "会员积分"},
			{Kind: domain.ViolationUndeclared, Property: "warranty_months"},
			{Kind: domain.ViolationRange, Property: "别的属性"},
		}
		cases = append(cases, c)
	}
	report, err := Mine(cases, o, DefaultMineOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.MissingFacts) != 1 {
		t.Fatalf("missing facts = %+v, want exactly the undeclared 会员积分", report.MissingFacts)
	}
	mf := report.MissingFacts[0]
	if mf.Property != "会员积分" || mf.Declared {
		t.Errorf("ticket = %+v, want undeclared 会员积分", mf)
	}
	if len(mf.Evidence) != 3 {
		t.Errorf("evidence = %v, want the three conversations", mf.Evidence)
	}
}

// TestCanonicalMentions_LongestMatchAndNoFigures pins two corroboration
// rules: a long label does not also count the shorter label inside it, and a
// numeric range value is a fact rather than wording, so quoting "24" names
// nothing.
func TestCanonicalMentions_LongestMatchAndNoFigures(t *testing.T) {
	o, err := domain.ParseYAML([]byte(`
product_line: T
properties:
  window_hours:
    label: 质量问题申报窗口
    range: {type: integer, unit: hour, values: ["24"]}
  arrival_hours:
    label: 退款到账时间
    range: {type: integer, unit: hour, values: ["24"]}
  precondition:
    label: 退款前置条件
    range:
      type: enum
      values: [quality_defect]
      labels: {quality_defect: 质量问题}
`))
	if err != nil {
		t.Fatal(err)
	}
	got := canonicalMentions(o, "这属于质量问题，签收24小时内申报即可")
	if len(got) != 1 || got[0] != domain.ValueAlias("precondition", "quality_defect") {
		t.Errorf("mentions = %v, want only the quality_defect value", got)
	}
	got = canonicalMentions(o, "请在质量问题申报窗口内提交")
	if len(got) != 1 || got[0] != domain.PropertyAlias("window_hours") {
		t.Errorf("mentions = %v, want only the long label's property", got)
	}
}
