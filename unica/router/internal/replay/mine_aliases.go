package replay

import (
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/kefu/unica/pkg/domain"
)

type declaredLabel struct {
	text string
	ref  domain.AliasRef
}

type labelSpan struct {
	start int
	end   int
	ref   domain.AliasRef
}

func declaredLabels(o *domain.Ontology) []declaredLabel {
	var out []declaredLabel
	add := func(text string, ref domain.AliasRef) {
		text = strings.TrimSpace(text)
		if utf8.RuneCountInString(text) < minPhraseRunes {
			return
		}
		out = append(out, declaredLabel{text: text, ref: ref})
	}
	for _, name := range o.PropertyNames() {
		p := o.Properties[name]
		ref := domain.PropertyAlias(name)
		add(name, ref)
		add(p.Label, ref)
		for _, v := range p.Range.Values {
			if _, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
				continue
			}
			vref := domain.ValueAlias(name, v)
			add(v, vref)
			add(p.Range.Display(v), vref)
		}
	}
	for _, name := range o.ClassNames() {
		ref := domain.ClassAlias(name)
		add(name, ref)
		add(o.Classes[name].Label, ref)
	}
	for _, d := range o.Denials {
		add(d.Term, domain.DenyAlias(d.Term))
	}
	return out
}

// canonicalMentions returns sorted declared refs mentioned in text using longest-match precedence.
func canonicalMentions(o *domain.Ontology, text string) []domain.AliasRef {
	if text == "" {
		return nil
	}
	var spans []labelSpan
	for _, dl := range declaredLabels(o) {
		for from := 0; from < len(text); {
			i := strings.Index(text[from:], dl.text)
			if i < 0 {
				break
			}
			start := from + i
			spans = append(spans, labelSpan{start: start, end: start + len(dl.text), ref: dl.ref})
			from = start + len(dl.text)
		}
	}
	seen := map[domain.AliasRef]bool{}
	for _, a := range spans {
		covered := false
		for _, b := range spans {
			if b.ref != a.ref && b.end-b.start > a.end-a.start && b.start <= a.start && a.end <= b.end {
				covered = true
				break
			}
		}
		if !covered {
			seen[a.ref] = true
		}
	}
	out := make([]domain.AliasRef, 0, len(seen))
	for r := range seen {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func isCanonicalOrAliased(o *domain.Ontology, phrase string) bool {
	norm := strings.ToLower(strings.TrimSpace(phrase))
	if norm == "" {
		return true
	}
	for _, dl := range declaredLabels(o) {
		if strings.ToLower(dl.text) == norm {
			return true
		}
	}
	for _, ref := range o.AliasRefs() {
		for _, existing := range o.Aliases[ref] {
			if strings.ToLower(strings.TrimSpace(existing)) == norm {
				return true
			}
		}
	}
	return false
}

type phraseCount struct {
	phrase  string
	count   int
	caseIDs []string
}

func frequentPhrases(cluster []Case) []phraseCount {
	seenBy := map[string]map[string]bool{}
	for _, c := range cluster {
		runes := []rune(c.Query)
		maxN := maxPhraseRunes
		if len(runes) < maxN {
			maxN = len(runes)
		}
		local := map[string]bool{}
		for n := minPhraseRunes; n <= maxN; n++ {
			for i := 0; i+n <= len(runes); i++ {
				p := string(runes[i : i+n])
				if strings.TrimSpace(p) == "" {
					continue
				}
				local[p] = true
			}
		}
		for p := range local {
			if seenBy[p] == nil {
				seenBy[p] = map[string]bool{}
			}
			seenBy[p][c.ID] = true
		}
	}

	var out []phraseCount
	for p, ids := range seenBy {
		if len(ids) < MinClusterSize {
			continue
		}
		list := make([]string, 0, len(ids))
		for id := range ids {
			list = append(list, id)
		}
		sort.Strings(list)
		out = append(out, phraseCount{phrase: p, count: len(list), caseIDs: list})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].count != out[j].count {
			return out[i].count > out[j].count
		}
		li, lj := utf8.RuneCountInString(out[i].phrase), utf8.RuneCountInString(out[j].phrase)
		if li != lj {
			return li > lj
		}
		return out[i].phrase < out[j].phrase
	})
	return out
}

func mineAliasCandidate(o *domain.Ontology, cluster []Case, caseByID map[string]Case) (*AliasCandidate, string) {
	candidates := frequentPhrases(cluster)
	if len(candidates) == 0 {
		return nil, "簇内没有出现次数达到成簇门槛的高频片段，可能需要人工归纳新的维度取值"
	}

	sawEvidence := false
	triedAny := false
	for _, cand := range candidates {
		if isCanonicalOrAliased(o, cand.phrase) {
			continue
		}
		triedAny = true

		refs := map[domain.AliasRef]bool{}
		var evidence []domain.ProposalEvidence
		for _, id := range cand.caseIDs {
			c, ok := caseByID[id]
			if !ok || strings.TrimSpace(c.Reference) == "" {
				continue
			}
			mentions := canonicalMentions(o, c.Reference)
			if len(mentions) == 0 {
				continue
			}
			for _, m := range mentions {
				refs[m] = true
			}
			evidence = append(evidence, domain.ProposalEvidence{
				ConversationID: c.ConversationID,
				Excerpt:        oneLine(c.Reference),
			})
		}
		if len(refs) == 0 {
			continue
		}
		sawEvidence = true
		if len(refs) > 1 {
			continue
		}
		var ref domain.AliasRef
		for r := range refs {
			ref = r
		}

		applied, err := domain.ApplyAlias(o, ref, cand.phrase)
		if err != nil || domain.ClassifyDiff(o, applied) != domain.TierAlias {
			continue
		}
		sort.Slice(evidence, func(i, j int) bool { return evidence[i].ConversationID < evidence[j].ConversationID })
		return &AliasCandidate{
			Ref:         ref,
			Phrase:      cand.phrase,
			Occurrences: cand.count,
			Evidence:    evidence,
		}, ""
	}
	if sawEvidence {
		return nil, "候选片段的坐席佐证指向了不止一个规范说法，有歧义，丢弃"
	}
	if !triedAny {
		return nil, "簇内的高频片段都已经是规范说法或已有别名，没有新词可提议"
	}
	return nil, "候选片段没有坐席参照回复佐证指向哪个规范说法，丢弃"
}
