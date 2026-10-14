package domain

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The three tag protocols a model is asked to emit — [FACT:...] claims,
// [INTENT:...] marketing signals and [HANDOFF:...] escalations — are recognised
// by the one scanner in this file rather than by a hand-written regular
// expression per protocol (D20).
//
// Each protocol used to carry its own pattern, and each pattern only knew ASCII
// brackets and the ASCII colon. A model writing through a Chinese input method
// produces 【HANDOFF:payout】 or [HANDOFF：payout] as readily as the documented
// form, and the consequences were the worst kind: the full-width bracket form
// was neither stripped nor acted on, so the customer read internal markup and a
// payout escalation reached nobody; the full-width colon form was stripped by
// the wide pattern but its value never parsed, so the agent lost the reason.
// Case had already been patched into all three patterns once. Patching width
// into all three again would be the fourth character-class patch, and the next
// variant would need a fifth — so the variants are absorbed here, once, and the
// protocols only ever see the canonical name and value.

// The protocol names, as ScanTags expects them.
const (
	TagFact    = "FACT"
	TagIntent  = "INTENT"
	TagHandoff = "HANDOFF"
)

// tagOpeners and tagClosers are the bracket forms accepted around a tag: ASCII,
// full-width square (［］), and the CJK lenticular brackets (【】) that Chinese
// input methods produce for "[" and "]". Any opener pairs with any closer: a
// model that opens with 【 and closes with ] still meant the tag, and the
// alternative is internal markup in a customer's message.
const (
	tagOpeners = `\[［【`
	tagClosers = `\]］】`
)

// tagSpanPattern builds the pattern for one protocol's tags, of any bracket
// width and spacing. Every protocol's pattern comes from this one template, so a
// variant accepted for one protocol is accepted for all three.
//
// After the name, the next character must not continue a word: [FACTORY] is
// text that happens to start with FACT, not a malformed tag, and stripping it
// would damage an answer. Anything else up to the first closer is the body — a
// colon and a value, nothing at all ([HANDOFF]), or a shape a model improvised
// ([HANDOFF payout]). The body may contain an opening bracket, because a value
// can ("[FACT:note=[a]]"); it stops at the first closer, as each per-protocol
// pattern did before.
//
// The pattern is per protocol rather than one alternation over the three names
// so that an unclosed tag of one protocol cannot hide a well-formed tag of
// another from that other protocol's scan. A per-protocol pattern alone does
// not stop the unclosed tag's own scan from stripping the follower, though;
// ScanTags guards that with nestedTagStart.
//
// RE2's \s is ASCII-only, so the ideographic space U+3000 is listed next to it.
func tagSpanPattern(name string) *regexp.Regexp {
	return regexp.MustCompile(
		`(?i)[` + tagOpeners + `][\s\x{3000}]*` + name +
			`(?:[^A-Za-z0-9_` + tagClosers + `][^` + tagClosers + `]*)?` +
			`[` + tagClosers + `]`)
}

var tagPatterns = map[string]*regexp.Regexp{
	TagFact:    tagSpanPattern(TagFact),
	TagIntent:  tagSpanPattern(TagIntent),
	TagHandoff: tagSpanPattern(TagHandoff),
}

// nestedTagStart finds, inside a matched span after its opener, the start of
// another tag of any of the three protocols.
//
// The body may contain an opening bracket, so an opener that is never closed
// runs on to the next closer — which can belong to a tag that follows, of this
// protocol or another. "【INTENT:price 好的 [HANDOFF:payout]" is then a single
// INTENT span, and because intents are parsed before escalations, stripping it
// deletes the HANDOFF tag before anything has read it: a payout escalation
// silently dropped. The per-protocol intent pattern this scanner replaced
// matched only a closed [a-z_]+ value and could not do that, so the shared
// scanner must not be what introduces it.
var nestedTagStart = regexp.MustCompile(
	`(?i)[` + tagOpeners + `][\s\x{3000}]*(?:` + TagFact + `|` + TagIntent + `|` + TagHandoff +
		`)(?:[^A-Za-z0-9_]|$)`)

// ProtocolTag is one tag as written, after normalisation.
type ProtocolTag struct {
	// Name is the protocol, upper-cased: TagFact, TagIntent or TagHandoff.
	Name string
	// Value is the text after the separator with surrounding space (including
	// U+3000) trimmed. Empty when there is no separator or nothing after it.
	Value string
	// Separated reports that a colon, half- or full-width, followed the name.
	// [HANDOFF] and [HANDOFF:] both have an empty Value; only the second is
	// Separated. A protocol that only accepts the documented shape checks it.
	Separated bool
}

// ScanTags finds every tag of the named protocol in answer and returns them in
// order, along with answer with all of them removed.
//
// Removal is total: every span that has the protocol's name in tag position is
// cut, whether or not its value parses. A half-written tag is a formatting slip
// that must still never reach the customer; whether it means anything is the
// protocol's call, made from the returned ProtocolTag. Tags of other protocols
// are left in place for their own parser.
//
// The returned text is not trimmed or re-spaced: each protocol tidies its
// output the way it always has, and this function changes which spans are
// recognised, not how the remainder is presented.
//
// An unknown protocol name is a programming error and panics, the same way a
// malformed pattern literal would at init.
func ScanTags(answer, name string) ([]ProtocolTag, string) {
	name = strings.ToUpper(name)
	pattern, ok := tagPatterns[name]
	if !ok {
		panic("domain.ScanTags: unknown tag protocol " + name)
	}
	var (
		tags []ProtocolTag
		out  strings.Builder
		pos  int
	)
	for pos < len(answer) {
		loc := pattern.FindStringIndex(answer[pos:])
		if loc == nil {
			break
		}
		start, end := pos+loc[0], pos+loc[1]
		_, openerSize := utf8.DecodeRuneInString(answer[start:])
		if inner := nestedTagStart.FindStringIndex(answer[start+openerSize : end]); inner != nil {
			// The opener at start was never closed before another tag began.
			// Leave it as text and resume at the inner tag, so that tag is
			// scanned (or left for its own protocol) instead of being cut
			// along with the unclosed one. The stray text stays visible,
			// exactly as it did before the scanner was shared; a dropped
			// escalation would not.
			next := start + openerSize + inner[0]
			out.WriteString(answer[pos:next])
			pos = next
			continue
		}
		out.WriteString(answer[pos:start])
		tags = append(tags, parseTagSpan(answer[start:end], name))
		pos = end
	}
	out.WriteString(answer[pos:])
	return tags, out.String()
}

// parseTagSpan splits a matched span into its separator and value. The span is
// known to match the protocol's pattern: one opener, optional space, the name
// in some letter case, a body, and one closer.
func parseTagSpan(span, name string) ProtocolTag {
	tag := ProtocolTag{Name: name}

	_, openerSize := utf8.DecodeRuneInString(span)
	_, closerSize := utf8.DecodeLastRuneInString(span)
	body := strings.TrimLeftFunc(span[openerSize:len(span)-closerSize], unicode.IsSpace)
	// The name matched case-insensitively but is ASCII, so its byte length is
	// the same in whatever case the model wrote it.
	body = strings.TrimLeftFunc(body[len(name):], unicode.IsSpace)

	switch {
	case strings.HasPrefix(body, ":"):
		tag.Separated, body = true, body[len(":"):]
	case strings.HasPrefix(body, "："):
		tag.Separated, body = true, body[len("："):]
	}
	if tag.Separated {
		tag.Value = strings.TrimFunc(body, unicode.IsSpace)
	}
	return tag
}

// SplitTagAssignment splits a "key=value" tag value at its first equals sign,
// half- or full-width, trimming space from both halves. ok is false when there
// is no equals sign at all.
func SplitTagAssignment(value string) (key, val string, ok bool) {
	i := strings.IndexAny(value, "=＝")
	if i < 0 {
		return "", "", false
	}
	_, sepSize := utf8.DecodeRuneInString(value[i:])
	return strings.TrimFunc(value[:i], unicode.IsSpace),
		strings.TrimFunc(value[i+sepSize:], unicode.IsSpace), true
}
