package domain

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Figure is one number stated next to a unit, found in free text.
type Figure struct {
	// Value is the number normalised for comparison: "6.50" and "6.5" are the
	// same figure, "六十" is "60", "1,000" is "1000".
	Value string
	// Unit is the unit as the table canonicalises it. Units are never
	// converted into each other — "8折" and "80%" stay different figures —
	// because a wrong equivalence would credit an unbacked number, while a
	// missing one only costs a false alarm a person clears.
	Unit string
	// Start and End are the byte offsets of the whole match in the source.
	Start, End int
	// Raw is the matched text exactly as written, for evidence.
	Raw string
	// Snippet is the match with a few characters of context on each side.
	Snippet string
}

// UnitTable decides which figures ExtractFigures reports and how it spells
// their units. The two tables exist because the replay's reference comparison
// and the commitment firewall want different things from the same scan: the
// former must keep reporting exactly what it always reported, or every
// historical comparison shifts under it, while the latter must see money
// written the way Chinese customer service writes it — "约60万元", "六十万",
// "百分之五".
type UnitTable struct {
	pattern *regexp.Regexp
	// canon maps a unit as written to the unit compared.
	canon map[string]string
	// wide enables Han numerals, "百分之N" and thousands separators.
	wide bool
}

// numberUnitAlt is the reference table's unit list. A bare number ("12315") is
// as likely to be an order or complaint reference as a fact; the unit is what
// turns a digit string into something a customer would notice disagreeing on.
const numberUnitAlt = `个月|小时|分钟|天|日|年|元|块|%|折|件|次|倍`

// moneyUnitAlt widens the list with the money and area units the reference
// table never needed. Longer spellings come first: Go's alternation is
// leftmost-first, so "万元" must be tried before "万" and "元".
const moneyUnitAlt = `个月|小时|分钟|万元|千元|亿元|元每|天|日|年|元|块|%|％|折|件|次|倍|万|亿|㎡`

// hanNumeralRun is a Han numeral as it is written before a unit. It must start
// with a digit or 十 so that "千万别担心" and "百般" are never read as numbers,
// and it leaves 万 and 亿 out of the run so they bind as units: "六十万元"
// reads as 60万, which is how the same figure is written in digits ("60万元").
const hanNumeralRun = `[一二两三四五六七八九十][零〇一二两三四五六七八九十百千]*`

// UnitsReference is the replay's table, byte-for-byte the pattern the
// reference comparison has always used.
var UnitsReference = UnitTable{
	pattern: regexp.MustCompile(`(\d+(?:\.\d+)?)(` + numberUnitAlt + `)`),
}

// UnitsMoney is the firewall's table.
var UnitsMoney = UnitTable{
	pattern: regexp.MustCompile(`(?:[¥￥]\s?)?(百分之(?:\d+(?:\.\d+)?|[零〇一二两三四五六七八九十百]+)|\d{1,3}(?:,\d{3})+(?:\.\d+)?|\d+(?:\.\d+)?|` +
		hanNumeralRun + `)(` + moneyUnitAlt + `)?`),
	canon: map[string]string{
		"块":  "元",
		"元每": "元",
		"万元": "万",
		"亿元": "亿",
		"％":  "%",
		// A day-count written 日 is the same duration as one written 天; the
		// date shapes that also end in 日 are excluded before this applies.
		"日": "天",
	},
	wide: true,
}

// idLikeContext matches a label that means "the number after this is an
// identifier, not a figure a customer would compare" — an order number, a
// ticket number, a phone number, a complaint reference. It is anchored to the
// end of the string it is tested against, i.e. it must sit immediately before
// the number.
var idLikeContext = regexp.MustCompile(`(?:编号|单号|订单|工单|电话|热线|手机|尾号|工号|投诉|快递)(?:号)?[:：\s]{0,2}$`)

// datePattern matches the two date shapes this codebase writes: "9月15日" and
// "2026-12-15". A date is not a figure to compare — "9月15日" and "9月16日"
// are two different days stated on purpose, not a policy dispute.
var datePattern = regexp.MustCompile(`\d{1,2}月\d{1,2}日|\d{4}-\d{1,2}-\d{1,2}`)

// placeholderPattern matches a redaction placeholder. A figure that lands
// inside or against one is masked data, not a policy figure.
var placeholderPattern = regexp.MustCompile(`〔[^〔〕]*〕`)

var (
	mobileShape   = regexp.MustCompile(`^1[3-9]\d{9}$`)
	landlineShape = regexp.MustCompile(`^0\d{9,11}$`)
)

// figureSpan is a half-open byte range [start, end).
type figureSpan struct{ start, end int }

func (s figureSpan) overlaps(o figureSpan) bool { return s.start < o.end && o.start < s.end }

// ExtractFigures finds every number-with-unit in s that survives the exclusion
// filters (identifier context, phone shape, date shape, redaction
// placeholder), in order of appearance.
func ExtractFigures(s string, units UnitTable) []Figure {
	if units.pattern == nil {
		return nil
	}
	idxs := units.pattern.FindAllStringSubmatchIndex(s, -1)
	if idxs == nil {
		return nil
	}
	var out []Figure
	for _, m := range idxs {
		start, end := m[0], m[1]
		numGroup := s[m[2]:m[3]]
		unit := ""
		if m[4] >= 0 {
			unit = s[m[4]:m[5]]
		}

		// A currency sign before the number ("退款￥500") is the unit. Only
		// the money table's pattern can match one; the reference table's
		// group 1 always starts the match.
		currency := m[2] > start

		value, unit, ok := units.normalise(s, m[3], numGroup, unit, currency)
		if !ok {
			continue
		}
		if isExcludedNumber(s, start, end, numGroup, unit) {
			continue
		}
		if c, ok := units.canon[unit]; ok {
			unit = c
		}
		out = append(out, Figure{
			Value:   value,
			Unit:    unit,
			Start:   start,
			End:     end,
			Raw:     s[start:end],
			Snippet: Snippet(s, start, end, 6),
		})
	}
	return out
}

// normalise turns the matched number into its comparable value. It reports
// false for a match that is not a figure at all: a number with no unit, or a
// Han numeral that only looks like one.
func (t UnitTable) normalise(s string, numEnd int, numGroup, unit string, currency bool) (string, string, bool) {
	if !t.wide {
		return normalizeNumber(numGroup), unit, true
	}
	if rest, ok := strings.CutPrefix(numGroup, "百分之"); ok {
		v, ok := parseNumberWord(rest)
		if !ok {
			return "", "", false
		}
		// "百分之五十元" is not a phrase anyone writes; the percentage is the
		// figure and whatever follows is the next word.
		return v, "%", true
	}
	if unit == "" && currency {
		unit = "元"
	}
	if unit == "" {
		return "", "", false
	}
	if isHanNumeral(numGroup) {
		// "一次性退还" is a manner, not a count: reading it as 1次 would make
		// every one-off refund promise look like an unbacked number.
		if numGroup == "一" && unit == "次" && strings.HasPrefix(s[numEnd+len(unit):], "性") {
			return "", "", false
		}
		v, ok := hanToNumber(numGroup)
		if !ok {
			return "", "", false
		}
		return v, unit, true
	}
	return normalizeNumber(strings.ReplaceAll(numGroup, ",", "")), unit, true
}

// isExcludedNumber reports whether a match is an identifier, a phone number,
// part of a date or part of a redaction placeholder rather than a figure.
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
	matchSpan := figureSpan{start, end}
	for _, m := range datePattern.FindAllStringIndex(s, -1) {
		if matchSpan.overlaps(figureSpan{m[0], m[1]}) {
			return true
		}
	}
	for _, m := range placeholderPattern.FindAllStringIndex(s, -1) {
		if matchSpan.overlaps(figureSpan{m[0], m[1]}) {
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

var hanDigits = map[rune]int{
	'零': 0, '〇': 0, '一': 1, '二': 2, '两': 2, '三': 3, '四': 4,
	'五': 5, '六': 6, '七': 7, '八': 8, '九': 9,
}

var hanMultipliers = map[rune]int{'十': 10, '百': 100, '千': 1000}

func isHanNumeral(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if _, ok := hanDigits[r]; ok {
			continue
		}
		if _, ok := hanMultipliers[r]; ok {
			continue
		}
		return false
	}
	return true
}

// parseNumberWord reads the N of "百分之N", which may be digits or Han.
func parseNumberWord(s string) (string, bool) {
	if s == "" {
		return "", false
	}
	if s == "百" {
		return "100", true
	}
	if isHanNumeral(s) {
		return hanToNumber(s)
	}
	if _, err := strconv.ParseFloat(s, 64); err != nil {
		return "", false
	}
	return normalizeNumber(s), true
}

// hanToNumber converts a Han numeral below ten thousand. A run with no
// multiplier is read digit by digit ("九五折" is 95折, the way it is
// spoken), which is also how the same figure is written in digits.
func hanToNumber(s string) (string, bool) {
	hasMultiplier := false
	for _, r := range s {
		if _, ok := hanMultipliers[r]; ok {
			hasMultiplier = true
			break
		}
	}
	if !hasMultiplier {
		var b strings.Builder
		for _, r := range s {
			d, ok := hanDigits[r]
			if !ok {
				return "", false
			}
			b.WriteByte(byte('0' + d))
		}
		v := strings.TrimLeft(b.String(), "0")
		if v == "" {
			v = "0"
		}
		return v, true
	}

	total, digit := 0, -1
	for _, r := range s {
		if d, ok := hanDigits[r]; ok {
			digit = d
			continue
		}
		mul := hanMultipliers[r]
		if digit < 0 {
			// "十五" is fifteen: a leading multiplier stands for one of it.
			digit = 1
		}
		total += digit * mul
		digit = -1
	}
	if digit > 0 {
		total += digit
	}
	return strconv.Itoa(total), true
}

// Snippet returns text around [start, end), expanded by up to radius runes on
// each side, so evidence quotes a match in context instead of bare digits.
func Snippet(text string, start, end, radius int) string {
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
