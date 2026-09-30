package domain

import (
	"strings"
	"testing"
)

const tryRunYAML = `
product_line: TryLine
classes:
  Goods: {label: 商品}
properties:
  return_window_days:
    label: 退货窗口
    domain: Goods
    range: {type: integer, unit: day}
    requires: [return_precondition]
  return_precondition:
    label: 退货前提
    range: {type: enum, values: [unopened], labels: {unopened: 未拆封}}
  delivery_method:
    label: 配送方式
    range: {type: enum, values: [express], labels: {express: 快递}}
assertions:
  - class: Goods
    values:
      return_window_days: 15
      return_precondition: unopened
  - values:
      delivery_method: express
denies:
  - term: 货到付款
    note: 仅支持线上支付
`

func tryRunOntology(t *testing.T) *Ontology {
	t.Helper()
	o, err := ParseYAML([]byte(tryRunYAML))
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}
	return o
}

func findProbe(t *testing.T, probes []Probe, kind ProbeKind, property string) Probe {
	t.Helper()
	for _, p := range probes {
		if p.Kind == kind && p.Property == property {
			return p
		}
	}
	t.Fatalf("no %s probe for %q among %d probes", kind, property, len(probes))
	return Probe{}
}

// TestDeriveProbes_OneQuestionPerFactDenialAndRequires pins the shape of the
// derived set: a fact probe per asserted property, a denial probe per denial,
// and a requires probe only for the property that actually declares one.
func TestDeriveProbes_OneQuestionPerFactDenialAndRequires(t *testing.T) {
	o := tryRunOntology(t)
	probes := DeriveProbes(o)

	var facts, denials, requires int
	for _, p := range probes {
		switch p.Kind {
		case ProbeFact:
			facts++
		case ProbeDenial:
			denials++
		case ProbeRequires:
			requires++
		}
	}
	if facts != 3 {
		t.Errorf("expected 3 fact probes (return_window_days, return_precondition, delivery_method), got %d", facts)
	}
	if denials != 1 {
		t.Errorf("expected 1 denial probe, got %d", denials)
	}
	if requires != 1 {
		t.Errorf("expected 1 requires probe (return_window_days only), got %d", requires)
	}

	rw := findProbe(t, probes, ProbeFact, "return_window_days")
	if !strings.Contains(rw.Question, "退货窗口") || !strings.Contains(rw.Question, "多久") {
		t.Errorf("unexpected fact question: %q", rw.Question)
	}
	if rw.Expect != "15天" {
		t.Errorf("expect should quote the ontology's own value, got %q", rw.Expect)
	}

	dm := findProbe(t, probes, ProbeFact, "delivery_method")
	if !strings.Contains(dm.Question, "配送方式") || !strings.Contains(dm.Question, "什么") {
		t.Errorf("unexpected fact question: %q", dm.Question)
	}

	den := findProbe(t, probes, ProbeDenial, "货到付款")
	if !strings.Contains(den.Question, "货到付款") {
		t.Errorf("unexpected denial question: %q", den.Question)
	}

	req := findProbe(t, probes, ProbeRequires, "return_window_days")
	if !strings.Contains(req.Expect, "退货前提") {
		t.Errorf("requires probe should name the withheld companion, got %q", req.Expect)
	}
}

// TestDeriveProbes_Deterministic guards the property the whole feature rests
// on: the same ontology always derives the same probes in the same order.
func TestDeriveProbes_Deterministic(t *testing.T) {
	o := tryRunOntology(t)
	first := DeriveProbes(o)
	second := DeriveProbes(o)

	if len(first) != len(second) {
		t.Fatalf("probe count changed between runs: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].ID != second[i].ID {
			t.Errorf("probe %d ID changed: %q vs %q", i, first[i].ID, second[i].ID)
		}
	}
}

// --- fact grading ---

func TestGradeProbe_Fact_PassesWhenAnswerMentionsTheOntologyValue(t *testing.T) {
	o := tryRunOntology(t)
	probe := findProbe(t, DeriveProbes(o), ProbeFact, "delivery_method")

	result := GradeProbe(o, probe, "我们通过快递配送。[FACT:delivery_method=express]", false)
	if result.Verdict != VerdictPass {
		t.Errorf("expected pass, got %s (%s)", result.Verdict, result.Reason)
	}
}

func TestGradeProbe_Fact_FailsWhenValueNeverMentioned(t *testing.T) {
	o := tryRunOntology(t)
	probe := findProbe(t, DeriveProbes(o), ProbeFact, "delivery_method")

	result := GradeProbe(o, probe, "这个我不太清楚，稍后回复您。", false)
	if result.Verdict != VerdictFail {
		t.Errorf("expected fail, got %s (%s)", result.Verdict, result.Reason)
	}
	if result.Reason != "未提到事实" {
		t.Errorf("unexpected reason: %q", result.Reason)
	}
}

func TestGradeProbe_Fact_FailsWhenAnswerContradictsOntology(t *testing.T) {
	o := tryRunOntology(t)
	probe := findProbe(t, DeriveProbes(o), ProbeFact, "delivery_method")

	result := GradeProbe(o, probe, "我们支持自提。[FACT:delivery_method=pickup]", false)
	if result.Verdict != VerdictFail {
		t.Errorf("expected fail, got %s (%s)", result.Verdict, result.Reason)
	}
}

func TestGradeProbe_Fact_EscalatedIsUnclear(t *testing.T) {
	o := tryRunOntology(t)
	probe := findProbe(t, DeriveProbes(o), ProbeFact, "delivery_method")

	result := GradeProbe(o, probe, "这个我帮您转接人工确认。", true)
	if result.Verdict != VerdictUnclear {
		t.Errorf("expected unclear, got %s (%s)", result.Verdict, result.Reason)
	}
}

// --- denial grading ---

func TestGradeProbe_Denial_PassesOnACorrectRefusal(t *testing.T) {
	o := tryRunOntology(t)
	probe := findProbe(t, DeriveProbes(o), ProbeDenial, "货到付款")

	result := GradeProbe(o, probe, "很抱歉，我们不支持货到付款，仅支持线上支付。", false)
	if result.Verdict != VerdictPass {
		t.Errorf("expected pass, got %s (%s)", result.Verdict, result.Reason)
	}
}

func TestGradeProbe_Denial_FailsWhenAnswerAffirmsTheDeniedCapability(t *testing.T) {
	o := tryRunOntology(t)
	probe := findProbe(t, DeriveProbes(o), ProbeDenial, "货到付款")

	result := GradeProbe(o, probe, "可以的，我们支持货到付款。", false)
	if result.Verdict != VerdictFail {
		t.Errorf("expected fail, got %s (%s)", result.Verdict, result.Reason)
	}
}

// --- requires grading ---

func TestGradeProbe_Requires_PassesWhenEscalated(t *testing.T) {
	o := tryRunOntology(t)
	probe := findProbe(t, DeriveProbes(o), ProbeRequires, "return_window_days")

	result := GradeProbe(o, probe, "这个需要人工核实，我帮您转接。", true)
	if result.Verdict != VerdictPass {
		t.Errorf("expected pass, got %s (%s)", result.Verdict, result.Reason)
	}
}

func TestGradeProbe_Requires_PassesWhenAnswerAsksBackForTheCompanion(t *testing.T) {
	o := tryRunOntology(t)
	probe := findProbe(t, DeriveProbes(o), ProbeRequires, "return_window_days")

	result := GradeProbe(o, probe, "请问是否满足退货前提？这会影响退货窗口天数。", false)
	if result.Verdict != VerdictPass {
		t.Errorf("expected pass, got %s (%s)", result.Verdict, result.Reason)
	}
}

func TestGradeProbe_Requires_FailsWhenAnswerJustStatesTheWindow(t *testing.T) {
	o := tryRunOntology(t)
	probe := findProbe(t, DeriveProbes(o), ProbeRequires, "return_window_days")

	result := GradeProbe(o, probe, "退货窗口是15天。[FACT:return_window_days=15]", false)
	if result.Verdict != VerdictFail {
		t.Errorf("expected fail, got %s (%s)", result.Verdict, result.Reason)
	}
}

func TestDeriveProbes_NilOntology(t *testing.T) {
	if probes := DeriveProbes(nil); probes != nil {
		t.Errorf("expected nil probes for nil ontology, got %v", probes)
	}
}
