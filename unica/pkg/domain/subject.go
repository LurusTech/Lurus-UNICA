package domain

import (
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Subject carry is the rule-based fix for D10 (doc/known-defects.md): a
// follow-up such as 物业费多少？ names no object, retrieval is stateless and
// returns whichever record scores best on 物业费 — another listing — and the
// model, whose own context says the conversation is about the first listing,
// attaches the other listing's figures to it. Nothing downstream catches that,
// because the misattributed figure really is in the retrieved passage.
//
// The fix prefixes such a follow-up with the object the customer last named,
// before retrieval and the model see it. It adds no model call and states no
// fact: the only text it adds is a name the customer typed, matched against a
// list a person wrote. Everything here is pure; the router decides when to
// call it and persists the subject in the multi-turn session.

// ConversationConfigKey is the product_lines.config_json key that holds the
// conversation block.
const ConversationConfigKey = "conversation"

// SubjectCarryMode is the subject-carry switch.
type SubjectCarryMode string

const (
	// SubjectCarryOff leaves every query exactly as the customer wrote it.
	SubjectCarryOff SubjectCarryMode = "off"
	// SubjectCarryShadow tracks the subject and counts the rewrites it would
	// make, while the model and retrieval still receive the original text.
	SubjectCarryShadow SubjectCarryMode = "shadow"
	// SubjectCarryOn sends the rewritten query to retrieval and the model.
	SubjectCarryOn SubjectCarryMode = "on"
)

// MaxSubjects caps the operator's subject list. Matching is a scan per entry
// on every message, and a list longer than this is a catalogue import rather
// than a list of things customers refer to.
const MaxSubjects = 500

// MaxSubjectRunes caps one subject name. The list is for names only, and the
// cap is what keeps a pasted description — which would carry figures into
// every rewritten query — from being accepted as one.
const MaxSubjectRunes = 30

// subjectMinRunes drops entries too short to identify anything. A one-rune
// entry matches inside unrelated words and would switch the subject on them.
const subjectMinRunes = 2

// shortFollowUpRunes is the longest query, punctuation aside, treated as a
// follow-up that leans on the conversation for its object.
const shortFollowUpRunes = 20

// subjectNamePunctuation is what a name never contains and a sentence does.
const subjectNamePunctuation = "，、。；：！？,;:!?\n\r\t"

// ParseSubjectCarryMode reads the switch. Empty is off; an unknown value is an
// error rather than a guess, matching the other mode parsers in this package.
func ParseSubjectCarryMode(s string) (SubjectCarryMode, error) {
	switch mode := SubjectCarryMode(strings.ToLower(strings.TrimSpace(s))); mode {
	case "":
		return SubjectCarryOff, nil
	case SubjectCarryOff, SubjectCarryShadow, SubjectCarryOn:
		return mode, nil
	default:
		return "", fmt.Errorf("unknown subject_carry mode %q (want off, shadow or on)", s)
	}
}

// ConversationConfig is the conversation block of config_json.
type ConversationConfig struct {
	SubjectCarry SubjectCarryMode `json:"subject_carry"`
	// Subjects are the objects customers of this line talk about — listings,
	// courses, shops — by name only, written by a person. They are the most
	// reliable half of the lexicon: one knowledge document often covers several
	// objects, so document names alone miss most of them.
	Subjects []string `json:"subjects"`
}

// Tracks reports whether the subject is followed at all (shadow or on).
func (c ConversationConfig) Tracks() bool {
	return c.SubjectCarry == SubjectCarryShadow || c.SubjectCarry == SubjectCarryOn
}

// Rewrites reports whether the rewritten query actually reaches the model.
func (c ConversationConfig) Rewrites() bool { return c.SubjectCarry == SubjectCarryOn }

type conversationWrapper struct {
	Conversation *ConversationConfig `json:"conversation"`
}

// LoadConversationConfig extracts the conversation block. Missing, malformed
// or unknown values all yield off, so a product line that has not opted in —
// or mistyped the switch — behaves exactly as before.
func LoadConversationConfig(configJSON json.RawMessage) ConversationConfig {
	off := ConversationConfig{SubjectCarry: SubjectCarryOff}
	if len(configJSON) == 0 {
		return off
	}
	var wrapper conversationWrapper
	if err := json.Unmarshal(configJSON, &wrapper); err != nil || wrapper.Conversation == nil {
		return off
	}
	cfg := *wrapper.Conversation
	mode, err := ParseSubjectCarryMode(string(cfg.SubjectCarry))
	if err != nil {
		log.Printf("[domain] %v; subject carry disabled for this product line", err)
		mode = SubjectCarryOff
	}
	cfg.SubjectCarry = mode
	cfg.Subjects = NormaliseSubjects(cfg.Subjects)
	return cfg
}

// NormaliseSubjects trims, drops blanks and removes duplicates (compared
// case-insensitively and ignoring spaces), keeping the first spelling and the
// operator's order.
func NormaliseSubjects(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		key := subjectKey(s)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, s)
	}
	return out
}

// ValidateSubjects checks an already normalised list against what the list
// is for: names, not descriptions. A name too short for the lexicon is
// refused rather than silently dropped, so the operator is not left believing
// an entry is recognised when it never can be.
func ValidateSubjects(subjects []string) error {
	if len(subjects) > MaxSubjects {
		return fmt.Errorf("对象名最多 %d 个，当前 %d 个", MaxSubjects, len(subjects))
	}
	for _, s := range subjects {
		if utf8.RuneCountInString(subjectKey(s)) < subjectMinRunes {
			return fmt.Errorf("对象名「%s」太短：至少 %d 个字，单个字会在无关的话里被误认成对象", s, subjectMinRunes)
		}
		if utf8.RuneCountInString(s) > MaxSubjectRunes {
			return fmt.Errorf("对象名「%s」超过 %d 个字：这里只填名称，不填介绍或数值", s, MaxSubjectRunes)
		}
		if strings.ContainsAny(s, subjectNamePunctuation) {
			return fmt.Errorf("对象名「%s」含有句读符号：这里只填名称，每行一个", s)
		}
	}
	return nil
}

// subjectKey is the comparison form: case-folded with all spaces removed, so
// "苏河湾 一号" and "苏河湾一号" are the same object.
func subjectKey(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if !unicode.IsSpace(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// lexiconEntry pairs the comparison form with the spelling used in a rewrite.
type lexiconEntry struct {
	key     string
	display string
}

// SubjectLexicon is the set of names a subject can be. Entries are ordered by
// length, longest first, so 华亭书苑二期 wins over 华亭书苑 where both match.
type SubjectLexicon struct {
	entries []lexiconEntry
}

// BuildSubjectLexicon assembles the lexicon from the operator's subject list,
// the ontology's dimension values (the class axis included, with labels and
// class aliases) and the stems of knowledge document names. Each source adds
// names only; none of them contributes a figure.
func BuildSubjectLexicon(subjects []string, o *Ontology, docNames []string) SubjectLexicon {
	var terms []string
	terms = append(terms, subjects...)
	if o != nil {
		for _, name := range o.ClassNames() {
			terms = append(terms, name, o.Classes[name].Label)
			terms = append(terms, o.Aliases[ClassAlias(name)]...)
		}
		for _, dim := range o.DimensionNames() {
			d := o.Dimensions[dim]
			for _, v := range d.Values {
				terms = append(terms, v, d.Display(v))
			}
		}
	}
	for _, name := range docNames {
		terms = append(terms, DocNameStem(name))
	}
	return SubjectLexicon{}.with(terms)
}

// with returns a lexicon extended by terms; the receiver is not modified.
func (l SubjectLexicon) with(terms []string) SubjectLexicon {
	entries := make([]lexiconEntry, 0, len(l.entries)+len(terms))
	seen := make(map[string]bool, len(l.entries)+len(terms))
	for _, e := range l.entries {
		seen[e.key] = true
		entries = append(entries, e)
	}
	for _, t := range terms {
		t = strings.TrimSpace(t)
		key := subjectKey(t)
		if utf8.RuneCountInString(key) < subjectMinRunes || seen[key] {
			continue
		}
		seen[key] = true
		entries = append(entries, lexiconEntry{key: key, display: t})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return utf8.RuneCountInString(entries[i].key) > utf8.RuneCountInString(entries[j].key)
	})
	return SubjectLexicon{entries: entries}
}

// Len is the number of entries.
func (l SubjectLexicon) Len() int { return len(l.entries) }

// Mentions returns the distinct entries text names, scanning left to right
// and taking the longest entry at each position, so 华亭书苑二期 is one mention
// and not also a mention of 华亭书苑.
func (l SubjectLexicon) Mentions(text string) []string {
	if len(l.entries) == 0 {
		return nil
	}
	norm := subjectKey(text)
	var found []string
	seen := map[string]bool{}
	for i := 0; i < len(norm); {
		matched := false
		for _, e := range l.entries {
			if strings.HasPrefix(norm[i:], e.key) {
				if !seen[e.key] {
					seen[e.key] = true
					found = append(found, e.display)
				}
				i += len(e.key)
				matched = true
				break
			}
		}
		if !matched {
			_, size := utf8.DecodeRuneInString(norm[i:])
			i += size
		}
	}
	return found
}

// Find returns the longest entry text names.
func (l SubjectLexicon) Find(text string) (string, bool) {
	norm := subjectKey(text)
	for _, e := range l.entries {
		if strings.Contains(norm, e.key) {
			return e.display, true
		}
	}
	return "", false
}

// deictics are the words a follow-up uses to point back at the object.
var deictics = []string{"那个", "这个", "它", "这套", "这款", "该", "上面", "刚才", "那边"}

// deicticFalseFriends contain a deictic without pointing at anything; they are
// removed before the deictic scan so 应该 does not read as 该.
var deicticFalseFriends = strings.NewReplacer("应该", "", "其它", "", "活该", "")

// NeedsSubject reports whether query leans on the conversation for its object:
// it names no lexicon entry, and it is either short or points back with a
// deictic. A query naming any entry — one object, or two in a comparison such
// as 华亭书苑和长风里弄哪个便宜 — already says what it is about and is never
// rewritten.
func NeedsSubject(query string, l SubjectLexicon) bool {
	if strings.TrimSpace(query) == "" || len(l.Mentions(query)) > 0 {
		return false
	}
	if contentRunes(query) <= shortFollowUpRunes {
		return true
	}
	scan := deicticFalseFriends.Replace(query)
	for _, d := range deictics {
		if strings.Contains(scan, d) {
			return true
		}
	}
	return false
}

// contentRunes counts letters and digits, ignoring punctuation and spaces.
func contentRunes(s string) int {
	n := 0
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			n++
		}
	}
	return n
}

// subjectCarryPrefix opens a rewritten query. It reads as a note rather than
// as the customer's words, so a model or a reviewer can tell which part was
// added.
const subjectCarryPrefix = "（接上文，指："

// CarrySubject returns query prefixed with the subject it refers to.
func CarrySubject(query, subject string) string {
	return subjectCarryPrefix + subject + "）" + query
}

// NextSubject decides the subject after a customer message. Only the
// customer's own words can set it: a message naming exactly one entry makes
// that entry the subject, one naming none keeps prev, and one naming several
// (a comparison) clears it, because guessing which of them a later 物业费多少
// means is the very error this exists to prevent.
//
// docNames are the documents retrieval returned for this message. Their stems
// join the lexicon for this decision only, so a listing that has its own
// document is recognised when the customer names it — but a stem the customer
// did not type cannot become the subject, and the AI's answer is never an
// input here at all: a name the model mentioned is not one the customer chose.
func NextSubject(prev string, l SubjectLexicon, customerMsg string, docNames []string) string {
	stems := make([]string, 0, len(docNames))
	for _, name := range docNames {
		stems = append(stems, DocNameStem(name))
	}
	switch mentions := l.with(stems).Mentions(customerMsg); len(mentions) {
	case 0:
		return prev
	case 1:
		return mentions[0]
	default:
		return ""
	}
}

// DocNameStem reduces a document name to the object it is about: the file
// extension and any qualifier after -, _ or an opening parenthesis are
// dropped, so 华亭书苑-户型介绍.pdf and 华亭书苑（二期）.md both yield 华亭书苑.
func DocNameStem(name string) string {
	name = strings.TrimSpace(name)
	if dot := strings.LastIndex(name, "."); dot > 0 && isFileExtension(name[dot+1:]) {
		name = name[:dot]
	}
	if cut := strings.IndexAny(name, "-_（("); cut >= 0 {
		name = name[:cut]
	}
	return strings.TrimSpace(name)
}

// isFileExtension accepts a short ASCII alphanumeric suffix, so a name such as
// 2.5 期 is not cut at its decimal point.
func isFileExtension(ext string) bool {
	if ext == "" || len(ext) > 5 {
		return false
	}
	hasLetter := false
	for _, r := range ext {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
			hasLetter = true
		case r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return hasLetter
}
