package replay

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/kefu/unica/pkg/domain"
	"github.com/kefu/unica/router/internal/guardrail"
)

// This file is stage 3-4 of the evolution loop: mining recurring handoffs on
// the training side into signatures, clustering them, and turning the
// clusters that qualify into the only two kinds of proposal a machine is
// trusted to write — tier A aliases (ApplyAlias, never a value) and tier C
// missing-fact tickets (never a value either, see domain.MissingFact). A
// cluster is never enough on its own: every alias candidate is re-validated
// through ApplyAlias/ClassifyDiff exactly as a person's own edit would be,
// so a bug in this file's own clustering can produce a bad suggestion but
// never a tier A row that is not actually tier A.
//
// There is deliberately no embedding or vector search here. A similarity
// metric can only be trusted once it has been checked against a real,
// labelled pull of conversations, and UNICA has none yet (doc/decisions —
// no live traffic to mine). Jaccard similarity over a signature's discrete
// features needs no such calibration: two identical feature sets are
// identical, and the threshold is at least legible to a reviewer reading the
// mining report, which a cosine cutoff on an opaque vector would not be. The
// interface is shaped so a real similarity function can replace jaccard
// later (see MineOptions) without touching anything downstream of it.

// DefaultSimilarityThreshold is the Jaccard similarity single-link
// clustering requires between two cases' signatures before it joins them,
// when MineOptions.SimilarityThreshold is left zero.
const DefaultSimilarityThreshold = 0.5

// MinClusterSize is how many cases a signature must recur in before mining
// treats it as a pattern worth acting on rather than noise. Fixed, not an
// option: the loop this file implements pinned the number itself.
const MinClusterSize = 3

// minPhraseRunes and maxPhraseRunes bound the frequent-phrase search over a
// cluster's customer messages. The lower bound matches domain's own
// aliasMinRunes (a one-character alias would match inside unrelated words);
// the upper bound keeps the substring scan (quadratic in message length)
// cheap without ever mattering in practice — a real alias phrase is a handful
// of characters, not a sentence.
const (
	minPhraseRunes = 2
	maxPhraseRunes = 12
)

// MineOptions tunes Mine. The zero value is not usable directly — call
// DefaultMineOptions, or set SimilarityThreshold explicitly.
type MineOptions struct {
	// SimilarityThreshold is the Jaccard similarity two cases' signatures
	// must reach to join the same cluster. Zero means
	// DefaultSimilarityThreshold.
	SimilarityThreshold float64
}

// DefaultMineOptions is what evolve propose uses.
func DefaultMineOptions() MineOptions {
	return MineOptions{SimilarityThreshold: DefaultSimilarityThreshold}
}

// ClusterReport is one signature cluster in the mining report.
type ClusterReport struct {
	ID int `json:"id"`
	// Size is how many handoffs share this signature; Share is that over
	// MineReport.TotalHandoffs.
	Size  int     `json:"size"`
	Share float64 `json:"share"`
	// Reasons lists the distinct logged handoff reasons in the cluster,
	// sorted. Forced reports whether any member is a forced reason: a
	// cluster mixing forced and reducible members is never mined for an
	// alias, the same caution GateWith applies to a single turn extended to
	// a whole cluster.
	Reasons []string `json:"reasons"`
	Forced  bool     `json:"forced"`
	// Representative is up to three customer messages from the cluster, for
	// a reviewer to recognise the pattern without reading every case.
	Representative []string `json:"representative,omitempty"`
}

// AliasCandidate is one tier A suggestion mined from a cluster: a customer
// phrase, and the single declared thing the agents' own replies corroborate
// it means. It carries no value and cannot: AliasRef only ever names
// something already declared (aliases.go), which is what makes it safe to
// generate without a person in the loop.
type AliasCandidate struct {
	ClusterID   int             `json:"cluster_id"`
	Ref         domain.AliasRef `json:"ref"`
	Phrase      string          `json:"phrase"`
	Occurrences int             `json:"occurrences"`
	// Share is the owning cluster's share of the training side's total
	// handoffs — what evolve propose ranks candidates by.
	Share    float64                   `json:"share"`
	Evidence []domain.ProposalEvidence `json:"evidence,omitempty"`
}

// DimensionHint flags a cluster that reached the size threshold but did not
// produce an alias — because its phrase was already declared, or no agent
// reply corroborated a single target, or several did and left it ambiguous.
// It is not a proposal of any tier; it is a pointer for a person deciding
// whether the cluster actually needs a new dimension value, a new class, or
// nothing at all — decisions tier B reserves for a human (proposal.go).
type DimensionHint struct {
	ClusterID      int      `json:"cluster_id"`
	Reason         string   `json:"reason"`
	Representative []string `json:"representative,omitempty"`
}

// MineReport is everything one mining pass over the training side found.
type MineReport struct {
	// TotalHandoffs is the denominator every Share in this report is over:
	// every training-side case that actually escalated, action-type
	// included.
	TotalHandoffs int `json:"total_handoffs"`

	// ActionCount and ActionShare cover handoffs that ask the AI to perform
	// an action (order status, logistics, refund progress) rather than
	// answer from policy. No ontology change can resolve one, so they are
	// reported and never clustered with the rest.
	ActionCount          int      `json:"action_count"`
	ActionShare          float64  `json:"action_share"`
	ActionRepresentative []string `json:"action_representative,omitempty"`

	// LongTailCount and LongTailShare are non-action handoffs that never
	// reached MinClusterSize with anything else — a shrinking long tail
	// share across mining rounds is itself a signal the loop is working.
	LongTailCount int     `json:"long_tail_count"`
	LongTailShare float64 `json:"long_tail_share"`

	Clusters        []ClusterReport      `json:"clusters,omitempty"`
	AliasCandidates []AliasCandidate     `json:"alias_candidates,omitempty"`
	MissingFacts    []domain.MissingFact `json:"missing_facts,omitempty"`
	DimensionHints  []DimensionHint      `json:"dimension_hints,omitempty"`
}

// Mine looks for recurring handoff patterns in cases and turns the ones that
// qualify into tier A alias candidates and tier C missing-fact tickets.
//
// cases must be training-side only. Mine checks every one of them against
// InHoldout itself and refuses the whole call if even one is on the frozen
// validation side — the isolation between what the miner learns from and
// what the gate judges on has to be physical, not a filter a caller could
// forget to apply.
func Mine(cases []Case, o *domain.Ontology, opts MineOptions) (*MineReport, error) {
	for _, c := range cases {
		if InHoldout(c.ConversationID) {
			return nil, fmt.Errorf(
				"mine: case %s (conversation %s) is on the holdout side; "+
					"mining must run only on the training split, never on the frozen validation set",
				c.ID, c.ConversationID)
		}
	}
	if o == nil {
		return nil, fmt.Errorf("mine: no ontology to mine against")
	}
	threshold := opts.SimilarityThreshold
	if threshold <= 0 {
		threshold = DefaultSimilarityThreshold
	}

	escalated := make([]Case, 0, len(cases))
	caseByID := make(map[string]Case, len(cases))
	for _, c := range cases {
		if !guardrail.Decision(c.Logged.Decision).Escalates() {
			continue
		}
		escalated = append(escalated, c)
		caseByID[c.ID] = c
	}
	sort.Slice(escalated, func(i, j int) bool { return escalated[i].ID < escalated[j].ID })

	report := &MineReport{TotalHandoffs: len(escalated)}
	if len(escalated) == 0 {
		return report, nil
	}

	var actionCases, clusterable []Case
	for _, c := range escalated {
		if isActionQuery(c.Query) {
			actionCases = append(actionCases, c)
		} else {
			clusterable = append(clusterable, c)
		}
	}
	report.ActionCount = len(actionCases)
	report.ActionShare = shareOf(len(actionCases), report.TotalHandoffs)
	report.ActionRepresentative = representativeQuotes(actionCases, 3)

	groups := clusterCases(o, clusterable, threshold)

	inCluster := map[string]bool{}
	var missingFacts []domain.MissingFact
	for _, group := range groups {
		if len(group) < MinClusterSize {
			continue
		}
		for _, c := range group {
			inCluster[c.ID] = true
		}

		classes := map[ReasonClass]bool{}
		reasons := map[string]bool{}
		for _, c := range group {
			classes[ClassifyReason(c.Logged.Reason)] = true
			reasons[c.Logged.Reason] = true
		}
		reasonList := make([]string, 0, len(reasons))
		for r := range reasons {
			reasonList = append(reasonList, r)
		}
		sort.Strings(reasonList)

		clusterID := len(report.Clusters) + 1
		cr := ClusterReport{
			ID:             clusterID,
			Size:           len(group),
			Share:          shareOf(len(group), report.TotalHandoffs),
			Reasons:        reasonList,
			Forced:         classes[Forced],
			Representative: representativeQuotes(group, 3),
		}
		report.Clusters = append(report.Clusters, cr)

		missingFacts = append(missingFacts, mineMissingFacts(o, group)...)

		// A forced reason must never be optimised away, and a cluster that
		// mixes a forced member with reducible ones is exactly the case
		// GateWith's own forced-handoff check exists to catch downstream —
		// mining refuses to even propose here rather than lean on the gate
		// to save it.
		pureReducible := len(classes) == 1 && classes[Reducible]
		if !pureReducible {
			continue
		}

		cand, whyNot := mineAliasCandidate(o, group, caseByID)
		if cand != nil {
			cand.ClusterID = clusterID
			cand.Share = cr.Share
			report.AliasCandidates = append(report.AliasCandidates, *cand)
			continue
		}
		report.DimensionHints = append(report.DimensionHints, DimensionHint{
			ClusterID:      clusterID,
			Reason:         whyNot,
			Representative: cr.Representative,
		})
	}

	report.MissingFacts = mergeMissingFacts(missingFacts)

	longTail := 0
	for _, c := range clusterable {
		if !inCluster[c.ID] {
			longTail++
		}
	}
	report.LongTailCount = longTail
	report.LongTailShare = shareOf(longTail, report.TotalHandoffs)

	return report, nil
}

func shareOf(n, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(n) / float64(total)
}

// representativeQuotes returns up to n customer messages, deterministically:
// cluster order is not meaningful (clusterCases only promises stable output,
// not an ordering a reviewer would recognise), so the quotes are picked by
// case ID rather than by whatever order grouping produced.
func representativeQuotes(cases []Case, n int) []string {
	sorted := append([]Case(nil), cases...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	var out []string
	for _, c := range sorted {
		if len(out) >= n {
			break
		}
		if q := oneLine(c.Query); q != "" {
			out = append(out, q)
		}
	}
	return out
}

// --- action-type detection ---------------------------------------------

// actionKeywords name a status the customer wants looked up rather than a
// policy question — nothing an ontology change can answer, however phrased.
var actionKeywords = []string{
	"订单号", "运单号", "快递单号", "工单号",
	"物流", "快递到哪", "到哪了", "到哪儿了", "配送到哪",
	"退款进度", "退货进度", "处理进度", "审核进度", "进度查",
	"查一下", "查下", "查询一下", "帮我查",
	"什么时候到", "多久能到", "多久到",
}

// actionPlaceholderPattern matches a bracketed placeholder standing in for an
// order or ticket number — the shape a redacted or templated "look this up
// for me" message takes even without one of actionKeywords' words.
var actionPlaceholderPattern = regexp.MustCompile(`[【\[〔(（]\s*(订单号|运单号|工单号|快递单号)\s*[】\]〕)）]`)

func isActionQuery(query string) bool {
	for _, kw := range actionKeywords {
		if strings.Contains(query, kw) {
			return true
		}
	}
	return actionPlaceholderPattern.MatchString(query)
}

// --- signatures and clustering ------------------------------------------

// signatureSet is one case's signature, as a set of opaque features so two
// signatures can be compared by Jaccard similarity without this file caring
// what kind of feature moved the score: the handoff reason and its class,
// which declared properties the customer's own message names, and the
// message's own character 2-4 grams.
func signatureSet(o *domain.Ontology, c Case) map[string]bool {
	set := map[string]bool{}
	set["reason:"+c.Logged.Reason] = true
	set["class:"+string(ClassifyReason(c.Logged.Reason))] = true
	for _, name := range propertyMentions(o, c.Query) {
		set["prop:"+name] = true
	}
	for g := range ngramSet(c.Query, 2, 4) {
		set["gram:"+g] = true
	}
	return set
}

func ngramSet(text string, minN, maxN int) map[string]bool {
	runes := []rune(text)
	out := map[string]bool{}
	for n := minN; n <= maxN && n <= len(runes); n++ {
		for i := 0; i+n <= len(runes); i++ {
			if g := string(runes[i : i+n]); strings.TrimSpace(g) != "" {
				out[g] = true
			}
		}
	}
	return out
}

func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 1
	}
	inter := 0
	for k := range a {
		if b[k] {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// unionFind is disjoint-set union, the standard structure for single-link
// clustering: two cases end up in the same component exactly when some chain
// of pairwise similarities at or above threshold connects them, which is
// what "single-link" means.
type unionFind struct{ parent []int }

func newUnionFind(n int) *unionFind {
	p := make([]int, n)
	for i := range p {
		p[i] = i
	}
	return &unionFind{parent: p}
}

func (u *unionFind) find(x int) int {
	for u.parent[x] != x {
		u.parent[x] = u.parent[u.parent[x]]
		x = u.parent[x]
	}
	return x
}

func (u *unionFind) union(a, b int) {
	ra, rb := u.find(a), u.find(b)
	if ra != rb {
		u.parent[ra] = rb
	}
}

// clusterCases groups cases by single-link Jaccard similarity over their
// signatures. The result's outer order and each group's inner order are both
// by case ID, so two calls on the same input, in whatever order the input
// arrived in, produce byte-identical output — Mine's own contract, and the
// reason this does not simply range over a map into the result.
func clusterCases(o *domain.Ontology, cases []Case, threshold float64) [][]Case {
	n := len(cases)
	if n == 0 {
		return nil
	}
	sigs := make([]map[string]bool, n)
	for i, c := range cases {
		sigs[i] = signatureSet(o, c)
	}
	uf := newUnionFind(n)
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			if jaccard(sigs[i], sigs[j]) >= threshold {
				uf.union(i, j)
			}
		}
	}

	groups := map[int][]Case{}
	for i, c := range cases {
		root := uf.find(i)
		groups[root] = append(groups[root], c)
	}
	out := make([][]Case, 0, len(groups))
	for _, g := range groups {
		sort.Slice(g, func(i, j int) bool { return g[i].ID < g[j].ID })
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0].ID < out[j][0].ID })
	return out
}

// --- declared vocabulary ---------------------------------------------------

// declaredLabel is one canonical wording the ontology already declares,
// together with what it names.
type declaredLabel struct {
	text string
	ref  domain.AliasRef
}

// declaredLabels rebuilds, from Ontology's exported fields, the same
// canonical-wording index package domain keeps to itself for authoring-time
// alias validation (canonicalOwners in aliases.go). Mining lives outside that
// package and needs the same association for a different purpose — telling
// a mined phrase apart from wording the ontology already has a name for.
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
			// A figure ("24", "10") is a fact, not wording: an agent quoting
			// it corroborates no customer phrase, and it recurs across
			// unrelated properties.
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

// propertyMentions returns, sorted, the declared property names whose own
// name or label appears in text — the signature's "which attribute this
// handoff is about" feature, and the same substring search checkClaims'
// undeclared-property check would have no use for here: Case, unlike Trace,
// carries no per-turn Violation, so this is the closest mining gets to it
// from an export alone.
func propertyMentions(o *domain.Ontology, text string) []string {
	if text == "" {
		return nil
	}
	seen := map[string]bool{}
	for _, name := range o.PropertyNames() {
		p := o.Properties[name]
		if strings.Contains(text, name) || (p.Label != "" && strings.Contains(text, p.Label)) {
			seen[name] = true
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// canonicalMentions returns, sorted, every declared thing text mentions by
// its canonical wording — used to read an agent's reply for corroboration of
// what a customer phrase means.
func canonicalMentions(o *domain.Ontology, text string) []domain.AliasRef {
	if text == "" {
		return nil
	}
	// Longest match wins: an agent writing a long label ("质量问题申报窗口")
	// has not also mentioned every shorter label inside it ("质量问题"), and
	// counting both would make every such reply look ambiguous.
	type span struct {
		start, end int
		ref        domain.AliasRef
	}
	var spans []span
	for _, dl := range declaredLabels(o) {
		for from := 0; from < len(text); {
			i := strings.Index(text[from:], dl.text)
			if i < 0 {
				break
			}
			start := from + i
			spans = append(spans, span{start, start + len(dl.text), dl.ref})
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

// isCanonicalOrAliased reports whether phrase is already something the
// ontology says out loud — its own declared wording, or a phrase already
// listed as someone's alias. Either way it needs no new alias.
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

// --- frequent phrases and alias mining -------------------------------------

// phraseCount is one substring that recurred across a cluster's customer
// messages, with the case IDs it recurred in.
type phraseCount struct {
	phrase  string
	count   int
	caseIDs []string
}

// frequentPhrases finds every substring of length [minPhraseRunes,
// maxPhraseRunes] that appears in at least MinClusterSize distinct cases'
// queries within the cluster, ordered most-recurring and longest first, then
// alphabetically — a fixed order so the caller's "try the best candidate
// first" always means the same candidate for the same input.
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

// mineAliasCandidate looks, in order, at a pure-reducible cluster's frequent
// phrases for the first one that qualifies as tier A: not already declared
// or aliased, and corroborated by exactly one declared target across the
// agent replies to the cases it occurs in. Ambiguous corroboration (more than
// one distinct target) or none at all disqualifies a phrase rather than
// guessing, and ApplyAlias/ClassifyDiff get the final say regardless — the
// same check a person's own edit would face.
//
// At most one candidate per cluster: the cluster's signature is one pattern,
// and proposing more than one wording for it at once is not "the smallest
// change that helps" any more.
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
			continue // ambiguous: more than one declared target corroborated
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
		// Every frequent phrase was already the ontology's own wording or an
		// existing alias — there was nothing new to corroborate, which reads
		// very differently to a reviewer than "we looked for evidence and
		// found none".
		return nil, "簇内的高频片段都已经是规范说法或已有别名，没有新词可提议"
	}
	return nil, "候选片段没有坐席参照回复佐证指向哪个规范说法，丢弃"
}

// --- missing-fact mining ----------------------------------------------------

// mineMissingFacts is tier C, from two signals:
//
//   - every declared property whose name or label an agent's own reply used,
//     but which the ontology asserts no value for at all;
//   - every property the live validator logged as undeclared_property — the
//     AI claimed something the ontology has no word for, which is where
//     undeclared_properties in violations/stats comes from too. The name is
//     the one the model used, not one mining made up, and the ticket asks a
//     person to declare it and supply its value.
//
// It never carries a value — the ticket only ever says a gap exists, matching
// MissingFact's own shape.
func mineMissingFacts(o *domain.Ontology, group []Case) []domain.MissingFact {
	hits := map[string]map[string]bool{}
	undeclared := map[string]map[string]bool{}
	for _, c := range group {
		for _, v := range c.LoggedViolations {
			name := strings.TrimSpace(v.Property)
			if v.Kind != domain.ViolationUndeclared || name == "" {
				continue
			}
			if _, declared := o.Properties[name]; declared {
				// Declared since the turn was judged; nothing is missing now.
				continue
			}
			if undeclared[name] == nil {
				undeclared[name] = map[string]bool{}
			}
			undeclared[name][c.ConversationID] = true
		}
	}
	for _, c := range group {
		if strings.TrimSpace(c.Reference) == "" {
			continue
		}
		for _, name := range mentionedUnassertedProperties(o, c.Reference) {
			if hits[name] == nil {
				hits[name] = map[string]bool{}
			}
			hits[name][c.ConversationID] = true
		}
	}
	names := make([]string, 0, len(hits))
	for n := range hits {
		names = append(names, n)
	}
	sort.Strings(names)

	out := make([]domain.MissingFact, 0, len(names))
	for _, n := range names {
		convs := make([]string, 0, len(hits[n]))
		for id := range hits[n] {
			convs = append(convs, id)
		}
		sort.Strings(convs)
		out = append(out, domain.MissingFact{
			Property: n,
			Declared: true,
			Evidence: convs,
			Reason: fmt.Sprintf("坐席参照回复提到「%s」，但本体的 assertions 里没有为它给出任何取值",
				o.Properties[n].Label),
		})
	}

	names = names[:0]
	for n := range undeclared {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		convs := make([]string, 0, len(undeclared[n]))
		for id := range undeclared[n] {
			convs = append(convs, id)
		}
		sort.Strings(convs)
		out = append(out, domain.MissingFact{
			Property: n,
			Declared: false,
			Evidence: convs,
			Reason:   fmt.Sprintf("AI 的答复宣称了本体未声明的「%s」，模型不该凭常识回答：请声明该属性并补上取值，或确认它不该回答", n),
		})
	}
	return out
}

func mentionedUnassertedProperties(o *domain.Ontology, text string) []string {
	var out []string
	for _, name := range o.PropertyNames() {
		p := o.Properties[name]
		if !strings.Contains(text, name) && !(p.Label != "" && strings.Contains(text, p.Label)) {
			continue
		}
		if propertyHasAssertion(o, name) {
			continue
		}
		out = append(out, name)
	}
	return out
}

func propertyHasAssertion(o *domain.Ontology, name string) bool {
	for _, a := range o.Assertions {
		if _, ok := a.Values[name]; ok {
			return true
		}
	}
	return false
}

// mergeMissingFacts combines tickets for the same property mined from
// different clusters into one, so evolve propose never queues the same gap
// twice in one round.
func mergeMissingFacts(facts []domain.MissingFact) []domain.MissingFact {
	byProperty := map[string]domain.MissingFact{}
	for _, f := range facts {
		existing, ok := byProperty[f.Property]
		if !ok {
			byProperty[f.Property] = f
			continue
		}
		existing.Evidence = mergeSortedUnique(existing.Evidence, f.Evidence)
		byProperty[f.Property] = existing
	}
	names := make([]string, 0, len(byProperty))
	for n := range byProperty {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]domain.MissingFact, 0, len(names))
	for _, n := range names {
		out = append(out, byProperty[n])
	}
	return out
}

func mergeSortedUnique(a, b []string) []string {
	set := map[string]bool{}
	for _, v := range a {
		set[v] = true
	}
	for _, v := range b {
		set[v] = true
	}
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// --- report rendering -------------------------------------------------------

// Markdown renders a mining report for a person deciding what to do with
// this round's output before evolve enqueue ever runs.
func (r MineReport) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# 本体挖掘报告\n\n")
	fmt.Fprintf(&b, "训练侧转人工总量：%d\n\n", r.TotalHandoffs)
	if r.TotalHandoffs == 0 {
		b.WriteString("没有可挖掘的转人工样本。\n")
		return b.String()
	}

	b.WriteString("| 分组 | 数量 | 占比 |\n|---|---|---|\n")
	fmt.Fprintf(&b, "| 动作类（需要查询，不进本体） | %d | %.1f%% |\n", r.ActionCount, 100*r.ActionShare)
	fmt.Fprintf(&b, "| 长尾（未成簇） | %d | %.1f%% |\n\n", r.LongTailCount, 100*r.LongTailShare)

	if len(r.ActionRepresentative) > 0 {
		b.WriteString("动作类样例：\n")
		for _, q := range r.ActionRepresentative {
			fmt.Fprintf(&b, "- %s\n", q)
		}
		b.WriteString("\n")
	}

	if len(r.Clusters) > 0 {
		b.WriteString("## 成簇（≥3 次）\n\n| 簇 | 数量 | 占比 | 原因 | 含强制类 |\n|---|---|---|---|---|\n")
		for _, c := range r.Clusters {
			forced := "否"
			if c.Forced {
				forced = "是，本簇只报告，绝不产出建议"
			}
			fmt.Fprintf(&b, "| %d | %d | %.1f%% | %s | %s |\n",
				c.ID, c.Size, 100*c.Share, strings.Join(c.Reasons, "、"), forced)
		}
		b.WriteString("\n")
		for _, c := range r.Clusters {
			if len(c.Representative) == 0 {
				continue
			}
			fmt.Fprintf(&b, "簇 %d 代表原话：\n", c.ID)
			for _, q := range c.Representative {
				fmt.Fprintf(&b, "- %s\n", q)
			}
			b.WriteString("\n")
		}
	}

	if len(r.AliasCandidates) > 0 {
		b.WriteString("## A 级别名候选\n\n| 簇 | 别名指向 | 客户原话片段 | 出现次数 | 簇占比 |\n|---|---|---|---|---|\n")
		for _, a := range r.AliasCandidates {
			fmt.Fprintf(&b, "| %d | %s | %s | %d | %.1f%% |\n",
				a.ClusterID, a.Ref, a.Phrase, a.Occurrences, 100*a.Share)
		}
		b.WriteString("\n")
	}

	if len(r.DimensionHints) > 0 {
		b.WriteString("## 疑似需要人工判断的簇（未自动产出别名）\n\n")
		for _, h := range r.DimensionHints {
			fmt.Fprintf(&b, "- 簇 %d：%s\n", h.ClusterID, h.Reason)
			for _, q := range h.Representative {
				fmt.Fprintf(&b, "  - %s\n", q)
			}
		}
		b.WriteString("\n")
	}

	if len(r.MissingFacts) > 0 {
		b.WriteString("## C 级缺事实工单\n\n| 属性 | 已声明 | 证据会话数 | 说明 |\n|---|---|---|---|\n")
		for _, m := range r.MissingFacts {
			fmt.Fprintf(&b, "| %s | %v | %d | %s |\n", m.Property, m.Declared, len(m.Evidence), m.Reason)
		}
		b.WriteString("\n")
	}

	return b.String()
}
