package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCheckCommitments(t *testing.T) {
	cases := []struct {
		name     string
		answer   string
		src      GroundingSources
		scope    CommitmentScope
		wantKind []ViolationKind
	}{
		{
			name:     "deferred figure: the passage says ask, the answer states a rate",
			answer:   "契税1.5%。",
			src:      GroundingSources{Retrieved: []string{"具体税费需咨询"}},
			wantKind: []ViolationKind{ViolationDeferredFigure},
		},
		{
			name:     "absolute word nobody wrote",
			answer:   "不满意可以全额退款。",
			src:      GroundingSources{Retrieved: []string{"签收后7天内可申请退款"}},
			wantKind: []ViolationKind{ViolationUnbackedCommitment},
		},
		{
			name:   "figure stated verbatim in the passage is backed",
			answer: "签约前解约可退还80%的服务费。",
			src:    GroundingSources{Retrieved: []string{"签约前解约，退还80%服务费"}},
		},
		{
			name:     "unbacked figure in a refund sentence",
			answer:   "签约前解约可退还90%的服务费。",
			src:      GroundingSources{Retrieved: []string{"签约前解约，退还80%服务费"}},
			wantKind: []ViolationKind{ViolationUnbackedCommitment},
		},
		{
			name:   "refusal before the cue chain exempts it",
			answer: "很抱歉，无法保证全额退款，需要人工审核。",
			src:    GroundingSources{},
		},
		{
			name:   "a question is not a promise",
			answer: "您是想申请全额退款吗？",
			src:    GroundingSources{},
		},
		{
			name:   "customer's own figure, repeated outside a promise, is theirs to have said",
			answer: "已查到您支付的199元订单。",
			src:    GroundingSources{CustomerMessage: "我付了199元，能退吗"},
			scope:  CommitmentScopeMoney,
		},
		{
			name:     "customer's own figure does not back a refund promise",
			answer:   "已收到，您支付的199元会原路退还。",
			src:      GroundingSources{CustomerMessage: "我付了199元，能退吗"},
			wantKind: []ViolationKind{ViolationUnbackedCommitment},
		},
		{
			name:     "a customer's demand does not back an absolute word",
			answer:   "好的，可以为您全额退款。",
			src:      GroundingSources{CustomerMessage: "我要求全额退款", Retrieved: []string{"签收后7天内可申请退款"}},
			wantKind: []ViolationKind{ViolationUnbackedCommitment},
		},
		{
			name:     "a customer's question does not back the rate the answer confirms",
			answer:   "可以退还100%。",
			src:      GroundingSources{CustomerMessage: "能退100%吗？"},
			wantKind: []ViolationKind{ViolationUnbackedCommitment},
		},
		{
			name:     "a customer's guess does not back a figure the knowledge base deferred",
			answer:   "契税1.5%。",
			src:      GroundingSources{CustomerMessage: "契税是1.5%吧", Retrieved: []string{"具体税费需咨询"}},
			wantKind: []ViolationKind{ViolationDeferredFigure},
		},
		{
			name:     "price outside a commitment: counted only under commitment scope",
			answer:   "车位约60万元/个。",
			src:      GroundingSources{Retrieved: []string{"车位可售，价格请到售楼处了解"}},
			wantKind: []ViolationKind{ViolationUngroundedFigure},
		},
		{
			name:     "price outside a commitment: held under money scope",
			answer:   "车位约60万元/个。",
			src:      GroundingSources{Retrieved: []string{"车位可售，价格请到售楼处了解"}},
			scope:    CommitmentScopeMoney,
			wantKind: []ViolationKind{ViolationUnbackedCommitment},
		},
		{
			name:     "a refund written with a currency sign is still a figure",
			answer:   "可退款￥500。",
			src:      GroundingSources{Retrieved: []string{"签收后7天内可申请退款"}},
			wantKind: []ViolationKind{ViolationUnbackedCommitment},
		},
		{
			name:   "a currency sign and 元 are the same unit",
			answer: "可退款￥500。",
			src:    GroundingSources{Retrieved: []string{"未开封可退款500元"}},
		},
		{
			name:   "Han numeral backed by digits",
			answer: "车位六十万元一个，保证无额外费用。",
			src:    GroundingSources{Retrieved: []string{"车位售价60万元"}},
		},
		{
			name:   "decimal point is not a sentence end",
			answer: "物业费3.6元/㎡/月，已含公摊。",
			src:    GroundingSources{Retrieved: []string{"物业费 3.6元/㎡/月"}},
		},
		{
			name:   "ontology backs the figure through its rendering",
			answer: "签约前解约可退还80%。",
			src: GroundingSources{Ontology: &Ontology{
				ProductLine: "T",
				Properties: map[string]Property{
					"refund_rate": {Label: "退费比例", Range: Range{Type: RangePercent}},
				},
				Assertions: []Assertion{{Values: map[string]StringList{"refund_rate": {"80"}}}},
			}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scope := tc.scope
			if scope == "" {
				scope = CommitmentScopeCommitment
			}
			got := CheckCommitments(tc.answer, nil, tc.src, scope)
			gotKinds := kindsOf(got)
			if len(gotKinds) != len(tc.wantKind) {
				t.Fatalf("kinds = %v, want %v (%+v)", gotKinds, tc.wantKind, got)
			}
			for i := range gotKinds {
				if gotKinds[i] != tc.wantKind[i] {
					t.Fatalf("kinds = %v, want %v (%+v)", gotKinds, tc.wantKind, got)
				}
			}
		})
	}
}

func TestCheckCommitments_EvidenceQuotesTheSentence(t *testing.T) {
	got := CheckCommitments("您好。契税1.5%，请知悉。", nil,
		GroundingSources{Retrieved: []string{"具体税费需咨询"}}, CommitmentScopeCommitment)
	if len(got) != 1 || got[0].Got != "1.5%" || !strings.Contains(got[0].Evidence, "契税1.5%") {
		t.Fatalf("got %+v", got)
	}
	detail := CommitmentDetail(got)
	if !strings.Contains(detail, "契税1.5%") || !strings.Contains(detail, "1.5%") {
		t.Errorf("detail must name the figure and its sentence: %q", detail)
	}
}

// A claim is credited only when the ontology itself asserts its value: the
// model writing a tag is not evidence.
func TestCheckCommitments_ClaimCreditNeedsAnAssertion(t *testing.T) {
	o := &Ontology{
		ProductLine: "T",
		Properties: map[string]Property{
			"refund_rate": {Label: "退费比例", Range: Range{Type: RangeInteger}},
			"other_rate":  {Label: "其他比例", Range: Range{Type: RangeInteger}},
		},
		Assertions: []Assertion{{Values: map[string]StringList{"refund_rate": {"70"}}}},
	}
	src := GroundingSources{Ontology: o}
	backed := CheckCommitments("可退还70%。", []Claim{{Property: "refund_rate", Value: "70"}}, src, CommitmentScopeCommitment)
	if len(backed) != 0 {
		t.Errorf("asserted claim value must back the figure: %+v", backed)
	}
	unbacked := CheckCommitments("可退还55%。", []Claim{{Property: "other_rate", Value: "55"}}, src, CommitmentScopeCommitment)
	if !hasKind(unbacked, ViolationUnbackedCommitment) {
		t.Errorf("a claim the ontology does not assert backs nothing: %+v", unbacked)
	}
}

func TestCreditableClaims(t *testing.T) {
	claims := []Claim{{Property: "a", Value: "1"}, {Property: "b", Value: "2"}}
	if got := CreditableClaims(claims, nil, false); got != nil {
		t.Errorf("unvalidated claims credit nothing: %+v", got)
	}
	got := CreditableClaims(claims, []Violation{{Kind: ViolationContradicts, Property: "a"}}, true)
	if len(got) != 1 || got[0].Property != "b" {
		t.Errorf("a violated claim must not be credited: %+v", got)
	}
}

func TestFirewallKinds_BlockingSplit(t *testing.T) {
	for _, k := range FirewallKinds() {
		if !IsFirewallKind(k) {
			t.Errorf("%s not recognised", k)
		}
	}
	for _, k := range []ViolationKind{ViolationUnbackedCommitment, ViolationDeferredFigure, ViolationRedline} {
		if !IsBlockingFinding(k) {
			t.Errorf("%s must be able to withhold", k)
		}
	}
	for _, k := range []ViolationKind{ViolationRedlineFlag, ViolationUngroundedFigure, ViolationContradicts} {
		if IsBlockingFinding(k) {
			t.Errorf("%s must never withhold through the firewall", k)
		}
	}
	if IsFirewallKind(ViolationDenied) {
		t.Error("ontology kinds are not firewall kinds")
	}
}

// TestNorthstar_ExcludesFirewallKinds pins the compliance false-positive query:
// a firewall hold that a reviewer marks false_positive must not count against
// the ontology validator.
func TestNorthstar_ExcludesFirewallKinds(t *testing.T) {
	for _, k := range FirewallKinds() {
		if !strings.Contains(enforcedReviewStatsSQL, "'"+string(k)+"'") {
			t.Errorf("enforced review stats does not exclude %s:\n%s", k, enforcedReviewStatsSQL)
		}
	}
	if !strings.Contains(enforcedReviewStatsSQL, "kind NOT IN") {
		t.Errorf("exclusion clause missing:\n%s", enforcedReviewStatsSQL)
	}
}

func TestCommitmentUnbackedIsForced(t *testing.T) {
	if got := ClassifyHandoffReason(HandoffReasonCommitmentUnbacked); got != ReasonForced {
		t.Errorf("commitment_unbacked classified %s, want forced", got)
	}
}

func TestCommitmentConfig_DefaultsOffAndParses(t *testing.T) {
	def := DefaultConfig()
	if def.CommitmentChecks() || def.CommitmentEnforces() {
		t.Error("the firewall must be off by default")
	}
	if def.Active() {
		t.Error("the default config must not load an ontology")
	}

	// A line configured before the firewall existed: off, and Active as before.
	old := LoadConfig(json.RawMessage(`{"ontology":{"inject_facts":true,"validation":"shadow"}}`))
	if old.Commitment != CommitmentOff || old.CommitmentScope != CommitmentScopeCommitment || old.CommitmentChecks() {
		t.Errorf("pre-firewall config must read as off: %+v", old)
	}

	typo := LoadConfig(json.RawMessage(`{"ontology":{"commitment":"enforcce","commitment_scope":"everything"}}`))
	if typo.Commitment != CommitmentOff || typo.CommitmentScope != CommitmentScopeCommitment {
		t.Errorf("unknown values must fall back to off/commitment on read: %+v", typo)
	}
	if typo.Active() {
		t.Error("the firewall alone does not make the ontology Active")
	}

	on := LoadConfig(json.RawMessage(`{"ontology":{"commitment":"enforce","commitment_scope":"money","redline_packs":["common"]}}`))
	if !on.CommitmentEnforces() || on.CommitmentScopeOrDefault() != CommitmentScopeMoney || len(on.RedlinePacks) != 1 {
		t.Errorf("configured firewall not read: %+v", on)
	}

	if _, err := ParseCommitmentMode("enforcce"); err == nil {
		t.Error("ParseCommitmentMode must reject an unknown value")
	}
	if m, err := ParseCommitmentMode(""); err != nil || m != CommitmentOff {
		t.Errorf("empty mode = %q, %v; want off", m, err)
	}
	if _, err := ParseCommitmentScope("all"); err == nil {
		t.Error("ParseCommitmentScope must reject an unknown value")
	}
	var nilCfg *Config
	if nilCfg.CommitmentChecks() || nilCfg.CommitmentEnforces() || nilCfg.CommitmentScopeOrDefault() != CommitmentScopeCommitment {
		t.Error("nil config must behave as off")
	}
}
