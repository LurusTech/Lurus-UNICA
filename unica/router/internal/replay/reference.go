package replay

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/kefu/unica/pkg/domain"
)

// span is a half-open byte range [start, end) into a matched string.
type span struct{ start, end int }

func (s span) overlaps(o span) bool { return s.start < o.end && o.start < s.end }

// numberUnitAlt lists the units a figure must sit directly next to before it
// is worth comparing. A bare number ("12315") is as likely to be an order or
// complaint reference as a fact, and both texts under comparison are free-form
// customer service prose, not structured data — the unit is what turns a
// digit string into something a customer would actually notice disagreeing on.
const numberUnitAlt = `个月|小时|分钟|天|日|年|元|块|%|折|件|次|倍`

var numberUnitPattern = regexp.MustCompile(`(\d+(?:\.\d+)?)(` + numberUnitAlt + `)`)

// idLikeContext matches a label that means "the number after this is an
// identifier, not a figure a customer would compare" — an order number, a
// ticket number, a phone number, a complaint reference. It is anchored to the
// end of the string it is tested against, i.e. it must sit immediately before
// the number.
var idLikeContext = regexp.MustCompile(`(?:编号|单号|订单|工单|电话|热线|手机|尾号|工号|投诉|快递)(?:号)?[:：\s]{0,2}$`)

// datePattern matches the two date shapes this codebase writes: "9月15日" and
// "2026-12-15". A date is not a figure to compare — "6.5" and "6.50" differ
// only in trailing zeros, but "9月15日" and "9月16日" are two different days
// stated on purpose, and neither text disagreeing there is a policy dispute.
var datePattern = regexp.MustCompile(`\d{1,2}月\d{1,2}日|\d{4}-\d{1,2}-\d{1,2}`)

// placeholderPattern matches a redaction placeholder. A figure that lands
// inside or against one is masked data, not a policy figure.
var placeholderPattern = regexp.MustCompile(`〔[^〔〕]*〕`)

var (
	mobileShape   = regexp.MustCompile(`^1[3-9]\d{9}$`)
	landlineShape = regexp.MustCompile(`^0\d{9,11}$`)
)

// CompareToReference is check (b): where an AI answer and the human agent's
// reply to the same turn disagree on a figure or on an enumerated value.
//
// It flags, it never decides. A human reply is not ground truth — agents
// improvise, and a reply may answer a follow-up the model never saw — so each
// flag is a line for a person to look at, and the check sits outside the gate.
// Every flag names its kind ([kind=number] or [kind=enum]) and quotes the
// evidence from both sides, so a reviewer spot-checking a sample can tell a
// real disagreement from a false positive without re-reading the whole turn.
//
// Two signals:
//   - Figures: a number stated with a unit (天/元/个月/... — an unlabelled
//     number is never compared, and neither is one that reads as an order,
//     ticket or phone number, or as part of a date). Flagged only when both
//     texts state a figure in the same unit and the two value sets do not
//     overlap at all — a reply that repeats one of the AI's figures alongside
//     others is not a disagreement.
//   - Enum values: for each enum property, the declared values (identifier or
//     wording) each text mentions, matched with a word boundary and with a
//     shorter declared value never credited for a span a longer declared
//     value already claims. Flagged only when both mention the property and
//     the two sets of mentioned values do not overlap at all.
func CompareToReference(o *domain.Ontology, answer, reference string) []string {
	if strings.TrimSpace(answer) == "" || strings.TrimSpace(reference) == "" {
		return nil
	}
	var out []string
	out = append(out, compareNumbers(answer, reference)...)
	out = append(out, compareEnums(o, answer, reference)...)
	return out
}

// numberHit is one number-with-unit survivor of the exclusion filters.
type numberHit struct {
	unit  string
	value string
	// text is a short snippet around the match, kept as evidence for a
	// reviewer rather than the bare digits.
	text string
}

func compareNumbers(answer, reference string) []string {
	aHits := numberUnitHits(answer)
	rHits := numberUnitHits(reference)
	if len(aHits) == 0 || len(rHits) == 0 {
		return nil
	}
	aByUnit := groupHits(aHits)
	rByUnit := groupHits(rHits)

	var units []string
	for u := range aByUnit {
		if _, ok := rByUnit[u]; ok {
			units = append(units, u)
		}
	}
	sort.Strings(units)

	var out []string
	for _, u := range units {
		a, r := aByUnit[u], rByUnit[u]
		if setsIntersect(a.values, r.values) {
			continue
		}
		out = append(out, fmt.Sprintf("[kind=number][单位=%s] 数值不一致：AI 说 %s，坐席说 %s",
			u, strings.Join(a.evidence, "、"), strings.Join(r.evidence, "、")))
	}
	return out
}

// numberUnitHits finds every number-with-unit in s that survives the
// exclusion filters (identifier context, phone shape, date shape, redaction
// placeholder).
func numberUnitHits(s string) []numberHit {
	idxs := numberUnitPattern.FindAllStringSubmatchIndex(s, -1)
	if idxs == nil {
		return nil
	}
	var out []numberHit
	for _, m := range idxs {
		start, end := m[0], m[1]
		numGroup := s[m[2]:m[3]]
		unit := s[m[4]:m[5]]
		if isExcludedNumber(s, start, end, numGroup, unit) {
			continue
		}
		out = append(out, numberHit{
			unit:  unit,
			value: normalizeNumber(numGroup),
			text:  snippet(s, start, end, 6),
		})
	}
	return out
}

func isExcludedNumber(s string, start, end int, numGroup, unit string) bool {
	if idLikeContext.MatchString(s[:start]) {
		return true
	}
	if mobileShape.MatchString(numGroup) || landlineShape.MatchString(numGroup) {
		return true
	}
	// A day-of-month reads "<month>月<day>日" and the month need not be a
	// digit — "本月15日", "每月5日", "下月20日" are all dates, not a duration
	// measured in days. The digit immediately preceded by "月" is always the
	// day-of-month regardless of how the month itself is written, so this
	// catches every such phrase datePattern's digit-only "x月x日" shape
	// misses, without touching an actual day-count like "7日内到账".
	if unit == "日" {
		if r, _ := utf8.DecodeLastRuneInString(s[:start]); r == '月' {
			return true
		}
	}
	matchSpan := span{start, end}
	for _, m := range datePattern.FindAllStringIndex(s, -1) {
		if matchSpan.overlaps(span{m[0], m[1]}) {
			return true
		}
	}
	for _, m := range placeholderPattern.FindAllStringIndex(s, -1) {
		if matchSpan.overlaps(span{m[0], m[1]}) {
			return true
		}
	}
	return false
}

// normalizeNumber treats 6.50 and 6.5 as the same figure, but 100 and 1 as
// different ones.
func normalizeNumber(raw string) string {
	if strings.Contains(raw, ".") {
		raw = strings.TrimSuffix(strings.TrimRight(raw, "0"), ".")
	}
	return raw
}

type unitGroup struct {
	values   map[string]bool
	evidence []string
}

func groupHits(hits []numberHit) map[string]*unitGroup {
	out := map[string]*unitGroup{}
	for _, h := range hits {
		g, ok := out[h.unit]
		if !ok {
			g = &unitGroup{values: map[string]bool{}}
			out[h.unit] = g
		}
		g.values[h.value] = true
		if !containsString(g.evidence, h.text) {
			g.evidence = append(g.evidence, h.text)
		}
	}
	return out
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func setsIntersect(a, b map[string]bool) bool {
	for k := range a {
		if b[k] {
			return true
		}
	}
	return false
}

// enumTerm is one way a declared value can be written: its identifier, or its
// customer-facing label, whichever wording differs from the other.
type enumTerm struct {
	value   string
	term    string
	isASCII bool
}

var asciiWordPattern = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// propertyTerms lists every term worth searching for. A single Han character
// is excluded on its own — "是" or "否" as a declared value's label would
// otherwise match almost any prose — and an identical (value, term) pair is
// only listed once.
func propertyTerms(prop domain.Property) []enumTerm {
	var terms []enumTerm
	seen := map[string]bool{}
	add := func(value, term string) {
		if term == "" || isSingleHanChar(term) {
			return
		}
		key := value + "\x00" + term
		if seen[key] {
			return
		}
		seen[key] = true
		terms = append(terms, enumTerm{value: value, term: term, isASCII: asciiWordPattern.MatchString(term)})
	}
	for _, v := range prop.Range.Values {
		add(v, v)
		if d := prop.Range.Display(v); d != v {
			add(v, d)
		}
	}
	return terms
}

func isSingleHanChar(s string) bool {
	r, size := utf8.DecodeRuneInString(s)
	return size == len(s) && unicode.Is(unicode.Han, r)
}

// enumMention is one declared value a text mentions, with a snippet of where.
type enumMention struct {
	Display  string
	Evidence string
}

// mentionedValues returns, in declaration order, the values of prop that text
// mentions. A term is credited to its value only where it has a word boundary
// (ASCII identifiers only — Han text has none) and where no longer term,
// belonging to any value of this same property, already claims that span:
// "延保" inside "延保服务" does not count as a mention of a shorter "延保"
// value once "延保服务" is itself declared.
func mentionedValues(prop domain.Property, text string) []enumMention {
	terms := propertyTerms(prop)
	if len(terms) == 0 || text == "" {
		return nil
	}
	sort.SliceStable(terms, func(i, j int) bool {
		return utf8.RuneCountInString(terms[i].term) > utf8.RuneCountInString(terms[j].term)
	})

	var claimed []span
	overlapsClaimed := func(sp span) bool {
		for _, c := range claimed {
			if sp.overlaps(c) {
				return true
			}
		}
		return false
	}

	evidence := map[string]string{}
	for _, t := range terms {
		for _, occ := range findAllOccurrences(text, t.term) {
			if t.isASCII && !asciiWordBoundaryOK(text, occ.start, occ.end) {
				continue
			}
			if overlapsClaimed(occ) {
				continue
			}
			claimed = append(claimed, occ)
			if _, seen := evidence[t.value]; !seen {
				evidence[t.value] = snippet(text, occ.start, occ.end, 6)
			}
		}
	}
	if len(evidence) == 0 {
		return nil
	}
	var out []enumMention
	for _, v := range prop.Range.Values {
		if ev, ok := evidence[v]; ok {
			out = append(out, enumMention{Display: prop.Range.Display(v), Evidence: ev})
		}
	}
	return out
}

func findAllOccurrences(text, term string) []span {
	var out []span
	if term == "" {
		return out
	}
	from := 0
	for {
		idx := strings.Index(text[from:], term)
		if idx < 0 {
			break
		}
		s := from + idx
		e := s + len(term)
		out = append(out, span{s, e})
		from = e
	}
	return out
}

func asciiWordBoundaryOK(text string, start, end int) bool {
	if start > 0 {
		r, _ := utf8.DecodeLastRuneInString(text[:start])
		if isWordRune(r) {
			return false
		}
	}
	if end < len(text) {
		r, _ := utf8.DecodeRuneInString(text[end:])
		if isWordRune(r) {
			return false
		}
	}
	return true
}

// isWordRune reports whether r would extend an ASCII identifier. It is
// deliberately ASCII-only: unicode.IsLetter also counts Han ideographs as
// letters, which would make every Chinese character next to an identifier
// look like it continues it, and no ASCII identifier in this codebase is
// ever glued to Chinese text without a separator.
func isWordRune(r rune) bool {
	return r == '_' ||
		(r >= 'a' && r <= 'z') ||
		(r >= 'A' && r <= 'Z') ||
		(r >= '0' && r <= '9')
}

func enumSetsIntersect(a, b []enumMention) bool {
	seen := map[string]bool{}
	for _, m := range a {
		seen[m.Display] = true
	}
	for _, m := range b {
		if seen[m.Display] {
			return true
		}
	}
	return false
}

func formatMentions(ms []enumMention) string {
	parts := make([]string, len(ms))
	for i, m := range ms {
		parts[i] = fmt.Sprintf("%s（%s）", m.Display, m.Evidence)
	}
	return strings.Join(parts, "、")
}

func compareEnums(o *domain.Ontology, answer, reference string) []string {
	if o == nil {
		return nil
	}
	var out []string
	for _, name := range o.PropertyNames() {
		prop := o.Properties[name]
		if prop.Range.Type != domain.RangeEnum || len(prop.Range.Values) < 2 {
			continue
		}
		inAnswer := mentionedValues(prop, answer)
		inRef := mentionedValues(prop, reference)
		if len(inAnswer) == 0 || len(inRef) == 0 || enumSetsIntersect(inAnswer, inRef) {
			continue
		}
		label := prop.Label
		if label == "" {
			label = name
		}
		out = append(out, fmt.Sprintf("[kind=enum][属性=%s] 不一致：AI 说 %s，坐席说 %s",
			label, formatMentions(inAnswer), formatMentions(inRef)))
	}
	return out
}

// snippet returns text around [start, end), expanded by up to radius runes on
// each side, so a flag quotes evidence a reviewer can read in context instead
// of a bare match.
func snippet(text string, start, end, radius int) string {
	lo := stepRunesBack(text, start, radius)
	hi := stepRunesForward(text, end, radius)
	s := strings.TrimSpace(text[lo:hi])
	if lo > 0 {
		s = "…" + s
	}
	if hi < len(text) {
		s = s + "…"
	}
	return s
}

func stepRunesBack(s string, byteIdx, n int) int {
	for i := 0; i < n && byteIdx > 0; i++ {
		_, size := utf8.DecodeLastRuneInString(s[:byteIdx])
		byteIdx -= size
	}
	return byteIdx
}

func stepRunesForward(s string, byteIdx, n int) int {
	for i := 0; i < n && byteIdx < len(s); i++ {
		_, size := utf8.DecodeRuneInString(s[byteIdx:])
		byteIdx += size
	}
	return byteIdx
}
