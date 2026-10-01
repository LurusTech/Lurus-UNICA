package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

// The fixtures below are demonstration data shaped after D10 in
// doc/known-defects.md, not real traffic.

func d10Lexicon() SubjectLexicon {
	// 华亭书苑 deliberately has no entry of its own: it is known only through
	// its document name, which is the path NextSubject has to cover.
	return BuildSubjectLexicon([]string{"苏河湾一号", "长风里弄"}, nil, nil)
}

// TestSubjectCarry_D10Conversation walks the D10 conversation turn by turn.
func TestSubjectCarry_D10Conversation(t *testing.T) {
	lex := d10Lexicon()

	// Turn 1: the customer names the listing; retrieval returns its document.
	subject := NextSubject("", lex, "华亭书苑还有房吗", []string{"华亭书苑.md", "苏河湾一号.md"})
	if subject != "华亭书苑" {
		t.Fatalf("turn 1 subject = %q, want 华亭书苑", subject)
	}

	// Turn 2: the follow-up without a subject is the one D10 lost.
	q2 := "物业费多少？车位怎么解决？"
	if !NeedsSubject(q2, lex) {
		t.Fatalf("NeedsSubject(%q) = false, want true", q2)
	}
	if got, want := CarrySubject(q2, subject), "（接上文，指：华亭书苑）物业费多少？车位怎么解决？"; got != want {
		t.Errorf("CarrySubject = %q, want %q", got, want)
	}
	if next := NextSubject(subject, lex, q2, []string{"苏河湾一号.md"}); next != "华亭书苑" {
		t.Errorf("a follow-up naming nothing moved the subject to %q", next)
	}

	// Turn 3: naming another listing switches the subject.
	q3 := "苏河湾一号呢"
	if NeedsSubject(q3, lex) {
		t.Errorf("NeedsSubject(%q) = true; a message naming its object must not be rewritten", q3)
	}
	if next := NextSubject(subject, lex, q3, nil); next != "苏河湾一号" {
		t.Errorf("turn 3 subject = %q, want 苏河湾一号", next)
	}
}

// A comparison names its own objects; rewriting it would point it at one of
// them, and the subject it leaves behind would be a guess.
func TestSubjectCarry_ComparisonIsNotRewritten(t *testing.T) {
	lex := BuildSubjectLexicon([]string{"华亭书苑", "长风里弄", "苏河湾一号"}, nil, nil)
	q := "华亭书苑和长风里弄哪个便宜"
	if NeedsSubject(q, lex) {
		t.Errorf("NeedsSubject(%q) = true, want false", q)
	}
	if got := lex.Mentions(q); len(got) != 2 {
		t.Errorf("Mentions(%q) = %v, want two entries", q, got)
	}
	if next := NextSubject("苏河湾一号", lex, q, nil); next != "" {
		t.Errorf("comparison left subject %q, want it cleared", next)
	}
}

// The subject comes from the customer only. A listing the AI mentioned is
// never an input, and a document name the customer did not type cannot be
// adopted through retrieval either.
func TestSubjectCarry_OnlyCustomerWordsSetTheSubject(t *testing.T) {
	lex := d10Lexicon()

	// NextSubject has no parameter for the AI answer, so an answer saying
	// 苏河湾一号也有类似户型 cannot move the subject; the router test pins that
	// end to end. What the customer said next names nothing, so it stays.
	if next := NextSubject("华亭书苑", lex, "好的", nil); next != "华亭书苑" {
		t.Errorf("subject moved to %q on a message naming nothing", next)
	}

	// 静安府 exists only as a retrieved document name; the customer never
	// said it, so it cannot become the subject.
	if next := NextSubject("华亭书苑", lex, "物业费多少", []string{"静安府.pdf"}); next != "华亭书苑" {
		t.Errorf("an unmentioned document name became the subject: %q", next)
	}
	if next := NextSubject("", lex, "物业费多少", []string{"静安府.pdf"}); next != "" {
		t.Errorf("an unmentioned document name became the subject: %q", next)
	}
}

func TestNeedsSubject(t *testing.T) {
	lex := d10Lexicon()
	long := "我想再确认一下关于交房时间和后续办理产权证需要准备哪些材料以及大概要多久能拿到"
	cases := []struct {
		query string
		want  bool
	}{
		{"那个多少钱", true},
		{"物业费多少？车位怎么解决？", true},
		{"苏河湾一号的物业费多少", false},
		{"苏河湾 一号呢", false},      // spacing does not hide a name
		{long, false},           // long, self-contained, no deictic
		{"这个" + long, true},     // long but points back
		{"我觉得应该" + long, false}, // 应该 is not 该
		{"", false},
		{"？？？", true}, // punctuation only counts as short
	}
	for _, c := range cases {
		if got := NeedsSubject(c.query, lex); got != c.want {
			t.Errorf("NeedsSubject(%q) = %v, want %v", c.query, got, c.want)
		}
	}
}

func TestSubjectLexicon_LongestMatch(t *testing.T) {
	lex := BuildSubjectLexicon([]string{"华亭书苑", "华亭书苑二期", "苏"}, nil, nil)
	if lex.Len() != 2 {
		t.Errorf("Len = %d, want 2 (one-rune entries are dropped)", lex.Len())
	}
	if got, _ := lex.Find("华亭书苑二期还有房吗"); got != "华亭书苑二期" {
		t.Errorf("Find = %q, want the longer entry", got)
	}
	if got := lex.Mentions("华亭书苑二期还有房吗"); len(got) != 1 || got[0] != "华亭书苑二期" {
		t.Errorf("Mentions = %v, want only the longer entry", got)
	}
	if _, ok := lex.Find("随便问问"); ok {
		t.Error("Find matched text naming no entry")
	}
}

func TestBuildSubjectLexicon_Sources(t *testing.T) {
	o := &Ontology{
		Classes: map[string]Class{"高层": {Label: "高层住宅"}},
		Dimensions: map[string]Dimension{
			"楼盘": {Label: "楼盘", Values: []string{"htsy"}, Labels: map[string]string{"htsy": "华亭书苑"}},
		},
		Aliases: map[AliasRef][]string{ClassAlias("高层"): {"电梯房"}},
	}
	lex := BuildSubjectLexicon([]string{"长风里弄"}, o, []string{"苏河湾一号-户型介绍.pdf"})
	for _, name := range []string{"长风里弄", "华亭书苑", "htsy", "高层住宅", "电梯房", "苏河湾一号"} {
		if _, ok := lex.Find("问一下" + name); !ok {
			t.Errorf("lexicon is missing %q", name)
		}
	}
}

func TestDocNameStem(t *testing.T) {
	cases := map[string]string{
		"华亭书苑.md":        "华亭书苑",
		"华亭书苑-户型介绍.pdf":  "华亭书苑",
		"华亭书苑（二期）.docx":  "华亭书苑",
		"华亭书苑_v2.txt":    "华亭书苑",
		"苏河湾一号":          "苏河湾一号",
		"长风里弄2.5期":       "长风里弄2.5期",
		" 长风里弄 (老洋房).md": "长风里弄",
	}
	for in, want := range cases {
		if got := DocNameStem(in); got != want {
			t.Errorf("DocNameStem(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLoadConversationConfig(t *testing.T) {
	cases := []struct {
		raw  string
		want SubjectCarryMode
	}{
		{``, SubjectCarryOff},
		{`{}`, SubjectCarryOff},
		{`{"guardrail":{}}`, SubjectCarryOff},
		{`{"conversation":{}}`, SubjectCarryOff},
		{`{"conversation":{"subject_carry":"shadow"}}`, SubjectCarryShadow},
		{`{"conversation":{"subject_carry":"ON"}}`, SubjectCarryOn},
		{`{"conversation":{"subject_carry":"enforce"}}`, SubjectCarryOff},
		{`not json`, SubjectCarryOff},
	}
	for _, c := range cases {
		cfg := LoadConversationConfig(json.RawMessage(c.raw))
		if cfg.SubjectCarry != c.want {
			t.Errorf("LoadConversationConfig(%q) mode = %q, want %q", c.raw, cfg.SubjectCarry, c.want)
		}
	}
	cfg := LoadConversationConfig(json.RawMessage(`{"conversation":{"subject_carry":"on","subjects":[" 华亭书苑 ","","华亭 书苑","长风里弄"]}}`))
	if strings.Join(cfg.Subjects, "|") != "华亭书苑|长风里弄" {
		t.Errorf("subjects = %v, want trimmed and deduplicated", cfg.Subjects)
	}
	if !cfg.Tracks() || !cfg.Rewrites() {
		t.Error("on must both track and rewrite")
	}
	if shadow := (ConversationConfig{SubjectCarry: SubjectCarryShadow}); !shadow.Tracks() || shadow.Rewrites() {
		t.Error("shadow must track without rewriting")
	}
	if off := (ConversationConfig{}); off.Tracks() || off.Rewrites() {
		t.Error("the zero value must be off")
	}
}

func TestValidateSubjects(t *testing.T) {
	if err := ValidateSubjects([]string{"华亭书苑", "Riverside 1"}); err != nil {
		t.Errorf("valid names rejected: %v", err)
	}
	if err := ValidateSubjects([]string{"华亭书苑，物业费1.5元每平"}); err == nil {
		t.Error("a description with sentence punctuation was accepted as a name")
	}
	if err := ValidateSubjects([]string{strings.Repeat("长", MaxSubjectRunes+1)}); err == nil {
		t.Error("an overlong name was accepted")
	}
	// Two names on one line read as one entry that customers never type.
	if err := ValidateSubjects([]string{"华亭书苑、苏河湾一号"}); err == nil {
		t.Error("an enumeration of names was accepted as one name")
	}
	// A one-rune name never reaches the lexicon; accepting it would let the
	// operator believe it is recognised.
	for _, short := range []string{"苏", "A", "苏 "} {
		if err := ValidateSubjects([]string{short}); err == nil {
			t.Errorf("a name too short to be recognised was accepted: %q", short)
		}
	}
	if err := ValidateSubjects([]string{"A座"}); err != nil {
		t.Errorf("a two-rune name was rejected: %v", err)
	}
	many := make([]string, MaxSubjects+1)
	for i := range many {
		many[i] = "对象" + strings.Repeat("甲", i%20) + string(rune('A'+i%26))
	}
	if err := ValidateSubjects(many); err == nil {
		t.Error("a list over the cap was accepted")
	}
}
