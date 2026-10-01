package domain

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// GroundingSources is everything that may back a figure in an answer.
//
// What is deliberately absent matters as much as what is here. Recalled
// experience notes are earlier AI answers: crediting them would let a figure
// the model once invented back the same figure forever after. The facts block
// is present only as the ontology itself, so the rendering is the one the
// model was shown.
type GroundingSources struct {
	// CustomerMessage is what the customer wrote this turn. A figure the
	// customer stated ("我付了199元") is theirs to have stated, and repeating
	// it back in passing is not the AI inventing it — so outside a commitment
	// it backs a figure.
	//
	// It never backs a promise, and never a figure the knowledge base handed
	// to a person. "我要求全额退款" answered with "可以为您全额退款" is the
	// exact failure the firewall exists for: the customer's demand is not the
	// business's policy, and crediting it would let any customer talk the AI
	// into a refund nobody wrote down just by naming it first.
	CustomerMessage string
	// Retrieved are the retrieval passages and the recalled knowledge
	// context, in their original wording.
	Retrieved []string
	// Ontology is the product line's active ontology, nil when none is loaded.
	Ontology *Ontology
}

// commitmentCues are the words that turn a sentence into something the
// business may be held to: money coming back, a guarantee, something being
// free or included. Bare "含" is left out on purpose — it sits inside 含义,
// 包含在内的说明 and a hundred other neutral phrases — so only the explicit
// 已含/包含 forms count.
var commitmentCues = []string{
	"退款", "退还", "退费", "赔偿", "赔付", "补偿", "返还", "免单", "全额",
	"包退", "包换", "保证", "承诺", "免费", "减免", "返现", "已含", "包含",
}

// absoluteWords promise without a number. Each must appear verbatim
// somewhere in the grounding before an answer may say it.
var absoluteWords = []string{"全额", "免费", "包邮", "无条件", "百分之百"}

// refusalForms turn a cue into its denial when they sit just before it.
// Bare "不" is not one of them: "不满意可全额退款" is a promise, and treating
// any 不 in the window as a denial would wave it through.
var refusalForms = []string{
	"不支持", "不提供", "无法", "不能", "不予", "不可", "不会", "没有", "并非",
	"不保证", "不承诺", "不包含", "不含", "不退", "不赔", "不在", "不属于", "不享受", "不适用",
}

// deferralPattern recognises a passage that hands a figure to a person: the
// knowledge base says "ask us", so any figure the answer supplies instead was
// not taken from it.
var deferralPattern = regexp.MustCompile(`需咨询|建议咨询|请咨询|以.{0,12}为准|另行核定|视具体情况|需按最新政策`)

// cueWindowRunes is how far before a cue a refusal form is looked for.
const cueWindowRunes = 4

// chainGapRunes is how far apart two cue words may sit and still be read as
// one phrase, so "无法保证全额退款" is one denied phrase and not a denied
// 保证 followed by an affirmed 退款.
const chainGapRunes = 2

// moneyUnits are the canonical units the money scope holds.
var moneyUnits = map[string]bool{"元": true, "%": true, "折": true, "万": true, "亿": true, "千元": true}

// CheckCommitments returns what the firewall finds in an answer.
//
// It never edits the answer and never proposes a corrected figure: the only
// outcomes are "backed" and "a person must look". Backed means the same value
// in the same unit appears somewhere in the grounding, or that a claim the
// ontology confirms carries that value. Backed does not mean correct — a
// figure lifted from the wrong passage is still backed — so this narrows what
// the AI can say unchecked; it does not certify what it says.
//
// claims must already have passed validation; a claim is credited only when
// the ontology itself asserts its value.
func CheckCommitments(answer string, claims []Claim, src GroundingSources, scope CommitmentScope) []Violation {
	if strings.TrimSpace(answer) == "" {
		return nil
	}
	// The knowledge corpus is what may back a promise; the customer's own
	// words are kept apart so they can back a figure only where no promise
	// or deferral is involved (see GroundingSources.CustomerMessage).
	corpus := knowledgeCorpus(src)
	backed := figureKeys(ExtractFigures(corpus, UnitsMoney))
	customerBacked := figureKeys(ExtractFigures(src.CustomerMessage, UnitsMoney))
	creditedValues := creditedClaimValues(src.Ontology, claims)
	deferred := false
	for _, r := range src.Retrieved {
		if deferralPattern.MatchString(r) {
			deferred = true
			break
		}
	}

	figures := ExtractFigures(answer, UnitsMoney)
	var out []Violation
	for _, sent := range splitSentences(answer) {
		var unbacked []Figure
		for _, f := range figures {
			if f.Start < sent.start || f.Start >= sent.end {
				continue
			}
			if backed[f.Unit+"\x00"+f.Value] || creditedValues[f.Value] {
				continue
			}
			unbacked = append(unbacked, f)
		}
		text := strings.TrimSpace(answer[sent.start:sent.end])

		if !sent.question {
			markers := findMarkers(answer, sent)
			if commitmentSentence(markers, figuresIn(figures, sent)) {
				var items []string
				for _, f := range unbacked {
					items = append(items, f.Raw)
				}
				for _, m := range markers {
					if m.absolute && !m.exempt && !strings.Contains(corpus, m.word) {
						items = appendUnique(items, m.word)
					}
				}
				if len(items) > 0 {
					out = append(out, Violation{
						Kind:     ViolationUnbackedCommitment,
						Got:      strings.Join(items, "、"),
						Evidence: text,
						Message: fmt.Sprintf("承诺句中的「%s」在本体和知识库原文中都找不到出处（客户自己提出的不算出处）",
							strings.Join(items, "、")),
					})
				}
				continue
			}
		}
		if len(unbacked) == 0 {
			continue
		}

		var money, other []string
		for _, f := range unbacked {
			if scope == CommitmentScopeMoney && moneyUnits[f.Unit] {
				money = append(money, f.Raw)
			} else {
				other = append(other, f.Raw)
			}
		}
		if deferred {
			// The knowledge base said a person decides this; every unbacked
			// figure in the answer is one the model supplied in their place.
			all := make([]string, 0, len(money)+len(other))
			all = append(append(all, money...), other...)
			out = append(out, Violation{
				Kind:     ViolationDeferredFigure,
				Got:      strings.Join(all, "、"),
				Evidence: text,
				Message: fmt.Sprintf("知识库写明此类数字需咨询或以实际核定为准，答复却给出了「%s」",
					strings.Join(all, "、")),
			})
			continue
		}
		money = dropCustomerStated(money, unbacked, customerBacked)
		other = dropCustomerStated(other, unbacked, customerBacked)
		if len(money) > 0 {
			out = append(out, Violation{
				Kind:     ViolationUnbackedCommitment,
				Got:      strings.Join(money, "、"),
				Evidence: text,
				Message:  fmt.Sprintf("金额或比例「%s」在本体、知识库原文和客户原话中都找不到出处", strings.Join(money, "、")),
			})
		}
		if len(other) > 0 {
			out = append(out, Violation{
				Kind:     ViolationUngroundedFigure,
				Got:      strings.Join(other, "、"),
				Evidence: text,
				Message:  fmt.Sprintf("数字「%s」找不到出处（仅计数）", strings.Join(other, "、")),
			})
		}
	}
	return out
}

// CreditableClaims keeps the claims validation did not object to, so the
// firewall can credit the values the ontology confirmed. Under validation off
// nothing was checked and nothing is credited.
func CreditableClaims(claims []Claim, violations []Violation, validated bool) []Claim {
	if !validated || len(claims) == 0 {
		return nil
	}
	bad := map[string]bool{}
	for _, v := range violations {
		bad[v.Property] = true
	}
	var out []Claim
	for _, c := range claims {
		if !bad[c.Property] {
			out = append(out, c)
		}
	}
	return out
}

// CommitmentPropertyViolations returns the ontology violations on properties
// marked Commitment. These are the ones the firewall still withholds while the
// breaker has switched ordinary enforcement off.
func CommitmentPropertyViolations(o *Ontology, violations []Violation) []Violation {
	if o == nil {
		return nil
	}
	var out []Violation
	for _, v := range violations {
		if prop, ok := o.Property(v.Property); ok && prop.Commitment {
			out = append(out, v)
		}
	}
	return out
}

// firewallKinds lists every kind the firewall writes. They stay out of the
// ontology quality statistics.
var firewallKinds = []ViolationKind{
	ViolationUnbackedCommitment, ViolationDeferredFigure,
	ViolationRedline, ViolationRedlineFlag, ViolationUngroundedFigure,
}

// FirewallKinds returns a copy of the firewall's violation kinds.
func FirewallKinds() []ViolationKind {
	return append([]ViolationKind(nil), firewallKinds...)
}

// IsFirewallKind reports whether a kind comes from the firewall rather than
// from the ontology.
func IsFirewallKind(k ViolationKind) bool {
	for _, f := range firewallKinds {
		if f == k {
			return true
		}
	}
	return false
}

// IsBlockingFinding reports whether a firewall finding may withhold an answer
// under enforce. Flag-only phrases and figures outside any commitment are
// evidence, not reasons to stop a reply.
func IsBlockingFinding(k ViolationKind) bool {
	switch k {
	case ViolationUnbackedCommitment, ViolationDeferredFigure, ViolationRedline:
		return true
	}
	return false
}

// firewallKindsSQL renders the firewall kinds as a SQL literal list. The
// values are compile-time constants of this package, never input.
func firewallKindsSQL() string {
	parts := make([]string, len(firewallKinds))
	for i, k := range firewallKinds {
		parts[i] = "'" + string(k) + "'"
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

// dropCustomerStated removes, from raws, the figures the customer stated
// this turn. raws holds the Raw text of figures drawn from unbacked, in
// order, so the figure each one names is looked up there.
func dropCustomerStated(raws []string, unbacked []Figure, customerBacked map[string]bool) []string {
	if len(raws) == 0 || len(customerBacked) == 0 {
		return raws
	}
	stated := map[string]bool{}
	for _, f := range unbacked {
		if customerBacked[f.Unit+"\x00"+f.Value] {
			stated[f.Raw] = true
		}
	}
	out := raws[:0:0]
	for _, r := range raws {
		if !stated[r] {
			out = append(out, r)
		}
	}
	return out
}

// knowledgeCorpus is the text a promise may be credited to: the retrieved
// passages and the ontology as rendered. The customer's message is not in it.
func knowledgeCorpus(src GroundingSources) string {
	parts := make([]string, 0, len(src.Retrieved)+1)
	parts = append(parts, src.Retrieved...)
	if src.Ontology != nil {
		parts = append(parts, Render(src.Ontology))
	}
	return strings.Join(parts, "\n")
}

func figureKeys(figs []Figure) map[string]bool {
	out := make(map[string]bool, len(figs))
	for _, f := range figs {
		out[f.Unit+"\x00"+f.Value] = true
	}
	return out
}

// creditedClaimValues collects the values of claims the ontology asserts. A
// claim's value carries no unit ("退费比例=80"), so it backs the bare value;
// the ontology having asserted exactly that value for that property is what
// makes the looser match acceptable.
func creditedClaimValues(o *Ontology, claims []Claim) map[string]bool {
	out := map[string]bool{}
	if o == nil {
		return out
	}
	for _, c := range claims {
		prop, ok := o.Property(c.Property)
		if !ok {
			continue
		}
		canonical := prop.Range.Canonical(c.Value)
		for _, entry := range o.FactsOf(c.Property) {
			if containsFold(entry.Values, canonical) {
				out[normalizeNumber(strings.TrimSpace(c.Value))] = true
				out[normalizeNumber(canonical)] = true
			}
		}
	}
	return out
}

func appendUnique(list []string, s string) []string {
	for _, v := range list {
		if v == s {
			return list
		}
	}
	return append(list, s)
}

// sentence is a half-open byte range of an answer.
type sentence struct {
	start, end int
	question   bool
}

// splitSentences cuts an answer at sentence enders. It differs from
// SentenceAround in one respect that matters here: an ASCII full stop between
// two digits is a decimal point, and cutting "1.5%" into "1" and "5%" would
// make every decimal figure unreadable.
func splitSentences(s string) []sentence {
	var out []sentence
	start := 0
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		end := false
		question := false
		switch r {
		case '。', '！', '；', '\n', '!', ';':
			end = true
		case '？', '?':
			end, question = true, true
		case '.':
			end = !(i > 0 && isDigitByte(s[i-1]) && i+1 < len(s) && isDigitByte(s[i+1]))
		}
		if end {
			out = append(out, finishSentence(s, start, i, question))
			start = i + size
		}
		i += size
	}
	if start < len(s) {
		out = append(out, finishSentence(s, start, len(s), false))
	}
	return out
}

func finishSentence(s string, start, end int, question bool) sentence {
	text := strings.TrimSpace(s[start:end])
	if strings.HasPrefix(text, "请问") || strings.HasSuffix(text, "吗") || strings.HasSuffix(text, "呢") {
		question = true
	}
	return sentence{start: start, end: end, question: question}
}

func isDigitByte(b byte) bool { return b >= '0' && b <= '9' }

func figuresIn(figs []Figure, s sentence) int {
	n := 0
	for _, f := range figs {
		if f.Start >= s.start && f.Start < s.end {
			n++
		}
	}
	return n
}

// marker is one cue or absolute word found in a sentence.
type marker struct {
	word       string
	start, end int
	cue        bool
	absolute   bool
	exempt     bool
}

// findMarkers locates cue and absolute words in one sentence and decides,
// chain by chain, whether a refusal form denies them.
func findMarkers(answer string, sent sentence) []marker {
	words := map[string]*marker{}
	for _, w := range commitmentCues {
		words[w] = &marker{word: w, cue: true}
	}
	for _, w := range absoluteWords {
		if m, ok := words[w]; ok {
			m.absolute = true
		} else {
			words[w] = &marker{word: w, absolute: true}
		}
	}
	ordered := make([]string, 0, len(words))
	for w := range words {
		ordered = append(ordered, w)
	}
	// Longest first, so 百分之百 is not also read as something shorter.
	sort.Slice(ordered, func(i, j int) bool {
		if len(ordered[i]) != len(ordered[j]) {
			return len(ordered[i]) > len(ordered[j])
		}
		return ordered[i] < ordered[j]
	})

	text := answer[sent.start:sent.end]
	var found []marker
	taken := func(a, b int) bool {
		for _, m := range found {
			if a < m.end && m.start < b {
				return true
			}
		}
		return false
	}
	for _, w := range ordered {
		from := 0
		for {
			idx := strings.Index(text[from:], w)
			if idx < 0 {
				break
			}
			a := sent.start + from + idx
			b := a + len(w)
			if !taken(a, b) {
				m := *words[w]
				m.start, m.end = a, b
				found = append(found, m)
			}
			from += idx + len(w)
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].start < found[j].start })

	// Group adjacent markers into chains and judge each chain once.
	for i := 0; i < len(found); {
		j := i + 1
		for j < len(found) && utf8.RuneCountInString(answer[found[j-1].end:found[j].start]) <= chainGapRunes {
			j++
		}
		lo := stepRunesBack(answer, found[i].start, cueWindowRunes)
		if lo < sent.start {
			lo = sent.start
		}
		window := answer[lo:found[j-1].end]
		exempt := containsAny(window, refusalForms)
		for k := i; k < j; k++ {
			found[k].exempt = exempt
		}
		i = j
	}
	return found
}

// commitmentSentence reports whether a sentence promises something: an
// undenied cue, plus a figure or an undenied absolute word.
func commitmentSentence(markers []marker, figures int) bool {
	cue, absolute := false, false
	for _, m := range markers {
		if m.exempt {
			continue
		}
		cue = cue || m.cue
		absolute = absolute || m.absolute
	}
	return cue && (figures > 0 || absolute)
}

// CommitmentDetail renders the findings that withheld an answer for the agent
// who receives it: each unbacked item next to the sentence it came from, so
// confirming or correcting the draft does not start with finding the problem.
func CommitmentDetail(findings []Violation) string {
	if len(findings) == 0 {
		return ""
	}
	parts := make([]string, 0, len(findings))
	for _, v := range findings {
		if !IsFirewallKind(v.Kind) {
			// An ontology violation on a commitment property already explains
			// itself in the agent's language.
			parts = append(parts, fmt.Sprintf("[%s] %s", v.Kind, v.Message))
			continue
		}
		item := v.Got
		if item == "" {
			item = v.Property
		}
		part := fmt.Sprintf("[%s] 「%s」", v.Kind, item)
		if v.Evidence != "" {
			part += "（原句：" + v.Evidence + "）"
		}
		parts = append(parts, part)
	}
	return "答复含未经本体或知识库背书的承诺、金额或红线表述，已拦下待人工确认后再发：" + strings.Join(parts, "；")
}
