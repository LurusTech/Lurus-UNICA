package domain

import "testing"

// invisible names the characters this predicate exists for. They are built from
// code points rather than written as literals because a literal U+FEFF in a Go
// source file is a compile error — and because a reviewer cannot see them, which
// is precisely why the first version of the emptiness check missed them.
var invisible = map[string]rune{
	"zero width space":            0x200B,
	"byte order mark":             0xFEFF,
	"word joiner":                 0x2060,
	"function application":        0x2061,
	"zero width non-joiner":       0x200C,
	"zero width joiner":           0x200D,
	"left-to-right mark":          0x200E,
	"right-to-left mark":          0x200F,
	"soft hyphen":                 0x00AD,
	"Mongolian vowel separator":   0x180E,
	"braille pattern blank":       0x2800,
	"Hangul filler":               0x3164,
	"halfwidth Hangul filler":     0xFFA0,
	"Hangul choseong filler":      0x115F,
	"Hangul jungseong filler":     0x1160,
	"Khmer vowel inherent AQ":     0x17B4,
	"combining grapheme joiner":   0x034F,
	"null":                        0x0000,
	"full-width space":            0x3000,
	"no-break space":              0x00A0,
	"narrow no-break space":       0x202F,
	"line separator":              0x2028,
	"paragraph separator":         0x2029,
	"next line":                   0x0085,
	"ogham space mark":            0x1680,
	"medium mathematical space":   0x205F,
	"four-per-em space":           0x2005,
	"zero width no-break padding": 0x2064,
}

// Every one of these renders as nothing. TrimSpace catches only the White_Space
// half of the list, which is how a "blank answer" check shipped that let a
// single zero width space through as a customer answer.
func TestIsBlankAnswer_InvisibleCharacters(t *testing.T) {
	for name, r := range invisible {
		s := string(r)
		if !IsBlankAnswer(s) {
			t.Errorf("%s (U+%04X) must count as blank", name, r)
		}
		// Mixed with ordinary whitespace, as a stripped tag leaves it.
		if !IsBlankAnswer(" \n\t" + s + "　") {
			t.Errorf("%s (U+%04X) mixed with whitespace must count as blank", name, r)
		}
		// And with real text it is not blank: the predicate must not eat answers.
		if IsBlankAnswer("支持七天退货" + s) {
			t.Errorf("%s (U+%04X) must not make a real answer blank", name, r)
		}
	}
}

// The residue shape: the answer was nothing but tag protocol, and stripping the
// tags leaves a separator behind. The customer receives one punctuation mark.
func TestIsBlankAnswer_TagResidue(t *testing.T) {
	for _, s := range []string{"", " ", "\n\n", "。", "、", "，", "-", "---", "-\n-", "]", "[", "**", ":", "：", "...", "……", "•", "|", "|---|---|", "``` ```"} {
		if !IsBlankAnswer(s) {
			t.Errorf("%q carries no answer for a customer; want blank", s)
		}
	}
}

// The other side of the trade-off, and the one that keeps this from suppressing
// real answers: terse is not blank. An emoji, a bare figure, a price or a single
// character verdict all told the customer something.
func TestIsBlankAnswer_TerseContentIsNotBlank(t *testing.T) {
	for _, s := range []string{"👍", "7", "7天", "¥199", "是", "OK", "支持。", "❌", "✅ 已受理"} {
		if IsBlankAnswer(s) {
			t.Errorf("%q says something; it must not be treated as blank", s)
		}
	}
}
