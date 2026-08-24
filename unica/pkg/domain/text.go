package domain

import "unicode"

// invisibleFillers are the characters that render as nothing without being
// White_Space and without being format controls, so neither unicode.IsSpace nor
// the Cf category catches them. They are listed one by one because there is no
// property that groups them: each is a letter or a symbol by category and a
// blank by appearance.
//
// U+180E moved out of White_Space in Unicode 6.3 and Go followed, which is
// exactly the kind of silent reclassification that makes a hand-written list
// safer here than a category test.
var invisibleFillers = map[rune]bool{
	0x115F: true, // Hangul choseong filler (Lo)
	0x1160: true, // Hangul jungseong filler (Lo)
	0x17B4: true, // Khmer vowel inherent AQ
	0x17B5: true, // Khmer vowel inherent AA
	0x180E: true, // Mongolian vowel separator
	0x2800: true, // Braille pattern blank (So)
	0x3164: true, // Hangul filler (Lo)
	0xFFA0: true, // Halfwidth Hangul filler (Lo)
}

// IsBlankAnswer reports whether a customer reading this text would learn
// nothing from it — that it is, from their side, the blank message D18 was
// about.
//
// It is deliberately wider than strings.TrimSpace(s) == "", which was the first
// attempt and which two independent adversarial passes walked straight through.
// TrimSpace only removes unicode.IsSpace, so a reply consisting of one zero
// width space (U+200B), a byte order mark (U+FEFF), a word joiner (U+2060) or a
// soft hyphen (U+00AD) counted as an answer and was delivered. That is not a
// theoretical attack: a BOM carried in from a knowledge-base chunk and a zero
// width separator emitted between a reasoning block and a reply block are both
// ordinary model output, and the model here is the reasoning model whose budget
// exhaustion caused D18 in the first place. Worse, the blank message then
// arrives with EmptyAnswerWithheld false and no metric — the dashboard reads
// "zero empty answers" while customers see the same blank as before.
//
// Punctuation counts as blank too, and that is a judgement call rather than a
// typographic fact. It is here because of the shape the tag protocols produce:
// an answer that was nothing but "[FACT:a=1]、[FACT:b=2]" or "[HANDOFF:payout]。"
// strips down to "、" or "。". The customer receives a single punctuation mark,
// which tells them precisely as much as a blank message did, and the
// send_and_handoff form of it is the very path D18 leaked through. The cost of
// being wrong in this direction is one unnecessary handoff on an answer no
// customer could have used; the cost of being wrong in the other direction is
// the defect this function exists to close.
//
// Sk and Sm join the blank side for the same reason: a stray "```" or "^" is
// markdown residue, and "|---|---|" is the skeleton of a table the model never
// filled in. What stays visible on purpose is letters, digits, So and Sc — an
// answer of "👍" or "¥199" is terse, not blank, and a model that replied with an
// emoji has at least said something. The line is drawn there and not further
// because past that point the predicate starts refusing answers instead of
// catching silence.
func IsBlankAnswer(s string) bool {
	for _, r := range s {
		switch {
		case unicode.IsSpace(r):
		case unicode.Is(unicode.Cc, r), unicode.Is(unicode.Cf, r):
		case unicode.Is(unicode.Mn, r), unicode.Is(unicode.Me, r):
		case unicode.IsPunct(r), unicode.Is(unicode.Sk, r), unicode.Is(unicode.Sm, r):
		case invisibleFillers[r]:
		default:
			return false
		}
	}
	return true
}
