package domain

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Redline actions. A withhold pack holds the answer under an enforcing
// firewall; a flag pack only records, for phrases that are wrong often enough
// to review and legitimate often enough that holding them would bury the
// queue (a risk-level disclosure says "低风险" for good reasons).
const (
	RedlineWithhold = "withhold"
	RedlineFlag     = "flag"
)

// RedlinePack is one embedded phrase list. Basis records why a phrase was
// listed when it was drafted; it is not a legal finding and the system makes
// none — a hit only means a person looks before the customer does.
type RedlinePack struct {
	ID     string        `yaml:"id" json:"id"`
	Label  string        `yaml:"label" json:"label"`
	Basis  string        `yaml:"basis" json:"basis,omitempty"`
	Action string        `yaml:"action" json:"action"`
	Terms  []RedlineTerm `yaml:"terms" json:"-"`
}

// RedlineTerm is a literal phrase or, when Pattern is set, a regular
// expression. When a pattern has a capturing group, the first group is the
// phrase that is reported and negation-checked; the rest of the match only
// rules out look-alikes ("确保本人" is not "保本"), since the regexp engine has
// no look-around. A pattern term may also set Term: it is then only the name
// the finding carries, so the review queue shows "保本" rather than a regex.
//
// Action, when set, narrows the pack's action for this one term. An industry
// pack mixes phrases that are wrong on their face (保本) with phrases that are
// often legitimate (低风险 in a disclosed risk grade); the latter may only be
// recorded. Narrowing goes one way: a term may not hold under a record-only
// pack, because the settings screen labels the whole pack by its action and an
// operator who chose "仅记录" must not find answers held.
type RedlineTerm struct {
	Term    string `yaml:"term"`
	Pattern string `yaml:"pattern"`
	Basis   string `yaml:"basis"`
	Action  string `yaml:"action"`

	re *regexp.Regexp
}

// label is how the term is named in a finding.
func (t RedlineTerm) label() string {
	if t.Term != "" {
		return t.Term
	}
	return t.Pattern
}

//go:embed redlinepacks/*.yaml
var redlineFS embed.FS

// builtinRedlines is loaded once, when the package initialises. The packs are
// compiled into the binary, so a broken one is a build defect: failing at
// start-up — and in every test of this package — is how it gets caught before
// a running process silently scans with half a pack.
var builtinRedlines = mustLoadBuiltinRedlines()

func mustLoadBuiltinRedlines() map[string]RedlinePack {
	packs, err := LoadRedlinePacks()
	if err != nil {
		panic("domain: embedded redline packs: " + err.Error())
	}
	return packs
}

// LoadRedlinePacks parses and compiles the embedded packs.
func LoadRedlinePacks() (map[string]RedlinePack, error) {
	return loadRedlinePacks(redlineFS, "redlinepacks")
}

func loadRedlinePacks(fsys fs.FS, dir string) (map[string]RedlinePack, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("read redline packs: %w", err)
	}
	out := map[string]RedlinePack{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		name := path.Join(dir, e.Name())
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		var pack RedlinePack
		if err := yaml.Unmarshal(data, &pack); err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		if err := pack.compile(); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if _, dup := out[pack.ID]; dup {
			return nil, fmt.Errorf("%s: duplicate pack id %q", name, pack.ID)
		}
		out[pack.ID] = pack
	}
	return out, nil
}

func (p *RedlinePack) compile() error {
	if strings.TrimSpace(p.ID) == "" {
		return fmt.Errorf("pack has no id")
	}
	if p.Action != RedlineWithhold && p.Action != RedlineFlag {
		return fmt.Errorf("pack %s: action %q (want withhold or flag)", p.ID, p.Action)
	}
	if len(p.Terms) == 0 {
		return fmt.Errorf("pack %s has no terms", p.ID)
	}
	for i := range p.Terms {
		t := &p.Terms[i]
		switch {
		case t.Pattern != "":
			re, err := regexp.Compile(t.Pattern)
			if err != nil {
				return fmt.Errorf("pack %s term %d: bad pattern %q: %w", p.ID, i+1, t.Pattern, err)
			}
			t.re = re
		case t.Term != "":
			t.re = regexp.MustCompile(regexp.QuoteMeta(t.Term))
		default:
			return fmt.Errorf("pack %s term %d has neither term nor pattern", p.ID, i+1)
		}
		switch t.Action {
		case "":
			t.Action = p.Action
		case RedlineFlag:
		case RedlineWithhold:
			if p.Action != RedlineWithhold {
				return fmt.Errorf("pack %s term %d: a record-only pack cannot hold an answer", p.ID, i+1)
			}
		default:
			return fmt.Errorf("pack %s term %d: action %q (want withhold or flag)", p.ID, i+1, t.Action)
		}
	}
	return nil
}

// mixed reports whether some terms of a withhold pack only record.
func (p RedlinePack) mixed() bool {
	for _, t := range p.Terms {
		if t.Action != p.Action {
			return true
		}
	}
	return false
}

// RedlinePackIDs lists the embedded pack ids, sorted.
func RedlinePackIDs() []string {
	ids := make([]string, 0, len(builtinRedlines))
	for id := range builtinRedlines {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// RedlinePackInfo describes a pack for a settings screen.
type RedlinePackInfo struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Action string `json:"action"`
	Basis  string `json:"basis,omitempty"`
	// Mixed marks a withhold pack some of whose terms only record, so the
	// screen does not promise that every phrase in it holds an answer.
	Mixed bool `json:"mixed,omitempty"`
}

// RedlinePacks lists the embedded packs for display, sorted by id.
func RedlinePacks() []RedlinePackInfo {
	ids := RedlinePackIDs()
	out := make([]RedlinePackInfo, 0, len(ids))
	for _, id := range ids {
		p := builtinRedlines[id]
		out = append(out, RedlinePackInfo{ID: p.ID, Label: p.Label, Action: p.Action, Basis: p.Basis, Mixed: p.mixed()})
	}
	return out
}

// redlineNegations deny a phrase when they sit just before it: "本产品不保本"
// names the phrase in order to rule it out.
var redlineNegations = []string{"不", "非", "并非", "无法", "不能", "不得", "禁止", "没有"}

// redlineNonNegations contain a negation character without negating
// anything. The single-character forms above would otherwise read
// "我非常肯定没问题" or "不仅包您满意" as a denial and wave the very phrase
// through, so these are blanked out of the window before it is checked.
var redlineNonNegations = strings.NewReplacer(
	"非常", "　", "不仅", "　", "不但", "　", "不光", "　", "不管", "　", "不论", "　", "不错", "　",
)

// negatedBefore reports whether window, the few characters before a phrase,
// denies it. Only the phrase's own clause counts: in "放心不会亏，保本的" the
// 不 denies 亏, not 保本, and reading it across the comma would wave the very
// phrase through.
func negatedBefore(window string) bool {
	if i := lastClauseBreak(window); i >= 0 {
		window = window[i:]
	}
	return containsAny(redlineNonNegations.Replace(window), redlineNegations)
}

// redlineClauseBreaks end a clause inside a sentence.
var redlineClauseBreaks = []string{"，", "、", "：", ",", ":"}

// lastClauseBreak returns the byte offset just past the last clause break in
// s, or -1 when there is none.
func lastClauseBreak(s string) int {
	at := -1
	for _, b := range redlineClauseBreaks {
		if i := strings.LastIndex(s, b); i >= 0 && i+len(b) > at {
			at = i + len(b)
		}
	}
	return at
}

// ScanRedlines finds the selected packs' phrases in an answer. Unknown pack
// ids are ignored: the admin API refuses them on write, and a pack retired in
// a later build must not break a line that still names it.
func ScanRedlines(answer string, packIDs []string) []Violation {
	var packs []RedlinePack
	seen := map[string]bool{}
	for _, id := range packIDs {
		if p, ok := builtinRedlines[id]; ok && !seen[id] {
			seen[id] = true
			packs = append(packs, p)
		}
	}
	return scanRedlines(answer, packs)
}

func scanRedlines(answer string, packs []RedlinePack) []Violation {
	if strings.TrimSpace(answer) == "" || len(packs) == 0 {
		return nil
	}
	sentences := splitSentences(answer)
	var out []Violation
	for _, p := range packs {
		for _, t := range p.Terms {
			if t.re == nil {
				continue
			}
			kind := ViolationRedline
			if t.Action == RedlineFlag {
				kind = ViolationRedlineFlag
			}
			for _, m := range t.re.FindAllStringSubmatchIndex(answer, -1) {
				start, end := m[0], m[1]
				if len(m) >= 4 && m[2] >= 0 {
					start, end = m[2], m[3]
				}
				sent, ok := sentenceAt(sentences, start)
				if !ok || sent.question {
					continue
				}
				lo := stepRunesBack(answer, start, cueWindowRunes)
				if lo < sent.start {
					lo = sent.start
				}
				if negatedBefore(answer[lo:start]) {
					continue
				}
				basis := t.Basis
				if basis == "" {
					basis = p.Basis
				}
				msg := fmt.Sprintf("命中红线表述「%s」（%s）", answer[start:end], p.Label)
				if basis != "" {
					msg += "；起草理由：" + basis
				}
				out = append(out, Violation{
					Kind:     kind,
					Property: t.label(),
					Scope:    p.ID,
					Got:      answer[start:end],
					Evidence: strings.TrimSpace(answer[sent.start:sent.end]),
					Message:  msg,
				})
				// One finding per term is enough for a person to act on.
				break
			}
		}
	}
	return out
}

func sentenceAt(sentences []sentence, pos int) (sentence, bool) {
	for _, s := range sentences {
		if pos >= s.start && pos < s.end {
			return s, true
		}
	}
	return sentence{}, false
}
