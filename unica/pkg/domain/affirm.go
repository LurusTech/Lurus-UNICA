package domain

import "strings"

// negationMarkers turn a mention of a capability into a denial of it.
//
// Bare "无" is deliberately absent: it appears inside "无理由", which is itself a
// frequent denial term, and would make every such mention self-negating.
var negationMarkers = []string{"不", "没", "无法", "未", "非", "暂", "抱歉", "遗憾", "并无"}

// interrogativeMarkers make a sentence a question, and a question about a
// capability is not an offer of it.
//
// This became necessary when the assistant started collecting case details
// before handing over. Intake questions restate the customer's situation by
// construction — asked to under-declare a contract price, the correct refusal
// is followed by "提出做低价格的是经纪人、卖方还是买方" so the agent inherits a
// filled ticket. That sentence carries no negation, so a mention-based check
// read the refusal as an offer and failed the one answer that got it right.
//
// Kept deliberately narrow. A real affirmation is declarative ("可以做低"), so
// admitting interrogatives costs little; admitting more would hollow out the
// check, which is a compliance control before it is a test assertion.
var interrogativeMarkers = []string{"？", "?", "请问", "是否", "还是"}

// sentenceEnders bound the window searched for a negation marker. Clause commas
// are excluded on purpose — "货到付款，我们这边不支持" denies across a comma.
var sentenceEnders = []string{"。", "！", "？", "；", "\n", ".", "!", "?", ";"}

// neutralised reports whether a sentence fails to present its contents as
// available — either by denying them or by asking about them.
func neutralised(sentence string) bool {
	return containsAny(sentence, negationMarkers) || containsAny(sentence, interrogativeMarkers)
}

// IsAffirmed reports whether term appears anywhere in text without a negation
// marker in the same sentence — that is, whether the text presents the term as
// something on offer.
//
// Limitation, accepted knowingly: negation is detected at sentence granularity,
// so "我们支持货到付款，配送时不收费" reads as a denial and slips through. Two
// things keep that acceptable. Structural claim checking catches the same error
// through the ontology whenever the model emits a [FACT:] tag, and every
// violation carries the sentence that triggered it, so a misjudgement is visible
// to the agent who receives the handoff.
func IsAffirmed(text, term string) bool {
	if term == "" {
		return false
	}
	offset := 0
	for {
		idx := strings.Index(text[offset:], term)
		if idx < 0 {
			return false
		}
		abs := offset + idx
		if !neutralised(SentenceAround(text, abs, len(term))) {
			return true
		}
		offset = abs + len(term)
	}
}

// SentenceAround returns the sentence enclosing text[start:start+length]. It is
// exported so a violation can quote the exact sentence that triggered it.
func SentenceAround(text string, start, length int) string {
	left := 0
	for _, sep := range sentenceEnders {
		if i := strings.LastIndex(text[:start], sep); i >= 0 && i+len(sep) > left {
			left = i + len(sep)
		}
	}

	right := len(text)
	tail := start + length
	for _, sep := range sentenceEnders {
		if i := strings.Index(text[tail:], sep); i >= 0 && tail+i < right {
			right = tail + i
		}
	}
	return text[left:right]
}

// AffirmingSentence returns the sentence in which term is affirmed, or an empty
// string when it is never affirmed.
func AffirmingSentence(text, term string) string {
	if term == "" {
		return ""
	}
	offset := 0
	for {
		idx := strings.Index(text[offset:], term)
		if idx < 0 {
			return ""
		}
		abs := offset + idx
		sentence := SentenceAround(text, abs, len(term))
		if !neutralised(sentence) {
			return sentence
		}
		offset = abs + len(term)
	}
}

func containsAny(s string, terms []string) bool {
	for _, t := range terms {
		if strings.Contains(s, t) {
			return true
		}
	}
	return false
}
