package checkup

import (
	"sort"
	"strings"
	"time"

	"github.com/kefu/unica/pkg/domain"
	"github.com/kefu/unica/pkg/guardrail"
	"github.com/kefu/unica/pkg/judge"
)

// Options configure Run.
type Options struct {
	// Line is the label on the report.
	Line string
	// Ontology is the business's facts, when it has written any. Without it
	// claim checking and the missing-fact scan by property are skipped and
	// the report says so.
	Ontology *domain.Ontology
	// Packs are the redline packs to scan with.
	Packs []string
	// Scope is the firewall scope; empty means commitment.
	Scope domain.CommitmentScope
	// Now stamps the report; nil means time.Now.
	Now func() time.Time
	// Version is the binary that wrote the report, so a forwarded report
	// says which rules produced it.
	Version string
}

// Finding is one thing the gate found in one reply.
type Finding struct {
	ConversationID string               `json:"conversation_id"`
	At             time.Time            `json:"at"`
	Speaker        string               `json:"speaker"`
	Query          string               `json:"query"`
	Reply          string               `json:"reply"`
	Kind           domain.ViolationKind `json:"kind"`
	Got            string               `json:"got,omitempty"`
	Message        string               `json:"message"`
	Evidence       string               `json:"evidence,omitempty"`
	// Blocking reports that enforcement would hold the reply for this.
	Blocking bool `json:"blocking"`
	// Pack is the redline pack, for redline findings.
	Pack string `json:"pack,omitempty"`
}

// MissingFactCluster groups unbacked promises by the promise word they were
// made under: "退款" promised with figures in eleven conversations and nothing
// written down is one fact the business should author. The cluster never
// carries a value — the figures agents quoted disagree with each other often
// enough that picking one would be the gate inventing a fact.
type MissingFactCluster struct {
	// Cue is the promise word (退款, 赔付, 质保...).
	Cue string `json:"cue"`
	// Property is the declared property the cue maps to, when the ontology
	// declares one that is mentioned alongside; empty otherwise.
	Property string `json:"property,omitempty"`
	// Conversations is how many distinct conversations made the promise.
	Conversations int `json:"conversations"`
	// Replies is how many replies did.
	Replies int `json:"replies"`
	// Examples are up to three sentences, redacted, as evidence.
	Examples []string `json:"examples"`
}

// SelfServe is the share of customer questions the written facts could
// answer directly. Present only with an ontology and a sufficient sample.
type SelfServe struct {
	CustomerTurns int     `json:"customer_turns"`
	Answerable    int     `json:"answerable"`
	Rate          float64 `json:"rate"`
	// Basis states the counting rule in the report's own words.
	Basis string `json:"basis"`
}

// Report is one checkup's data.
type Report struct {
	Line        string    `json:"line"`
	GeneratedAt time.Time `json:"generated_at"`
	Version     string    `json:"version,omitempty"`
	Packs       []string  `json:"packs"`
	HasOntology bool      `json:"has_ontology"`

	Conversations int `json:"conversations"`
	CustomerTurns int `json:"customer_turns"`
	Replies       int `json:"replies"`
	AIReplies     int `json:"ai_replies"`
	HumanReplies  int `json:"human_replies"`

	// WouldWithhold is how many replies enforcement would have held.
	WouldWithhold int `json:"would_withhold"`
	// Flagged is how many replies carry a record-only finding and nothing
	// that would hold them.
	Flagged int `json:"flagged"`

	// Sufficient reports whether Replies reached MinSamples; rates appear
	// only when it did.
	Sufficient bool `json:"sufficient"`
	MinSamples int  `json:"min_samples"`
	// WouldWithholdRate is WouldWithhold/Replies, nil unless Sufficient.
	WouldWithholdRate *float64 `json:"would_withhold_rate,omitempty"`

	Unbacked     []Finding            `json:"unbacked"`
	Redlines     []Finding            `json:"redlines"`
	Violations   []Finding            `json:"violations"`
	MissingFacts []MissingFactCluster `json:"missing_facts"`
	// UnassertedProperties are declared properties customers asked about
	// that no assertion gives a value for. Ontology only.
	UnassertedProperties []domain.MissingFact `json:"unasserted_properties,omitempty"`
	SelfServe            *SelfServe           `json:"self_serve,omitempty"`

	// PackCases are the packs' own adversarial cases, judged here so the
	// reader can see the gate catch what it claims to; they are not samples
	// and are never added to any count above.
	PackCases   []domain.PackCaseResult `json:"pack_cases"`
	PackReports []PackReport            `json:"pack_reports"`
}

// PackReport is a selected pack's report wording.
type PackReport struct {
	ID    string            `json:"id"`
	Label string            `json:"label"`
	Rep   domain.PackReport `json:"report"`
}

// checkupGuardrail is the send-or-handoff settings a checkup judges under:
// the defaults with the confidence threshold off. An export has no
// retrieval score for a person's reply, and a rate of "low confidence
// handoffs" over replies that never had a confidence would be noise
// presented as a finding.
func checkupGuardrail() *guardrail.Config {
	c := guardrail.Defaults()
	c.ConfidenceThreshold = 0
	return c
}

// Run judges every reply in convs and assembles the report data. It never
// writes; the caller renders.
func Run(convs []Conversation, o Options) Report {
	now := o.Now
	if now == nil {
		now = time.Now
	}
	if o.Scope == "" {
		o.Scope = domain.CommitmentScopeCommitment
	}
	c := Report{
		Line: o.Line, GeneratedAt: now(), Version: o.Version, Packs: append([]string(nil), o.Packs...),
		HasOntology: o.Ontology != nil, MinSamples: judge.MinShadowSamples,
		Unbacked: []Finding{}, Redlines: []Finding{}, Violations: []Finding{},
		MissingFacts: []MissingFactCluster{}, PackCases: []domain.PackCaseResult{},
	}
	cfg := judge.ModeConfig(judge.ModeShadow, o.Packs, o.Scope)
	gcfg := checkupGuardrail()
	ev := guardrail.NewEvaluator()
	cues := cueGroups(o.Packs)

	type clusterAcc struct {
		convs    map[string]bool
		replies  int
		examples []string
		property string
	}
	clusters := map[string]*clusterAcc{}
	unasserted := map[string]*domain.MissingFact{}
	answerable := 0

	for _, conv := range convs {
		c.Conversations++
		msgs := append([]Message(nil), conv.Messages...)
		sortIfTimed(msgs)
		query := ""
		for _, m := range msgs {
			text := strings.TrimSpace(m.Text)
			if text == "" {
				continue
			}
			switch m.Sender {
			case SenderCustomer:
				query = text
				c.CustomerTurns++
				if o.Ontology != nil {
					if len(o.Ontology.MentionedProperties(text)) > 0 && !domain.IsActionQuery(text) {
						answerable++
					}
					for _, name := range o.Ontology.MentionedUnassertedProperties(text) {
						mf := unasserted[name]
						if mf == nil {
							mf = &domain.MissingFact{Property: name, Declared: true,
								Reason: "客户问到了这个属性，但本体没有给出它的值"}
							unasserted[name] = mf
						}
						if len(mf.Evidence) == 0 || mf.Evidence[len(mf.Evidence)-1] != conv.ConversationID {
							mf.Evidence = append(mf.Evidence, conv.ConversationID)
						}
					}
				}
				continue
			case SenderAI, SenderHuman:
			default:
				continue
			}

			c.Replies++
			if m.Sender == SenderAI {
				c.AIReplies++
			} else {
				c.HumanReplies++
			}
			s := judge.JudgeShadowed(judge.Input{
				Query: query, Answer: text, Ontology: o.Ontology, OntologyCfg: cfg,
				GuardrailCfg: gcfg, TriageMode: guardrail.TriageOff,
				FactsInjected: o.Ontology != nil, ProductLineID: o.Line,
			}, ev)
			j := s.Judgement
			base := Finding{ConversationID: conv.ConversationID, At: m.At, Speaker: m.Sender, Query: query, Reply: j.Answer}

			flagged := false
			for _, v := range j.Violations {
				f := base
				f.Kind, f.Got, f.Message, f.Evidence, f.Blocking = v.Kind, v.Got, v.Message, v.Evidence, true
				c.Violations = append(c.Violations, f)
			}
			for _, v := range j.Commitments {
				f := base
				f.Kind, f.Got, f.Message, f.Evidence = v.Kind, v.Got, v.Message, v.Evidence
				f.Blocking = domain.IsBlockingFinding(v.Kind)
				switch v.Kind {
				case domain.ViolationUnbackedCommitment, domain.ViolationDeferredFigure:
					c.Unbacked = append(c.Unbacked, f)
					if cue := cueIn(v.Evidence, v.Got, cues); cue != "" {
						acc := clusters[cue]
						if acc == nil {
							acc = &clusterAcc{convs: map[string]bool{}}
							clusters[cue] = acc
						}
						acc.convs[conv.ConversationID] = true
						acc.replies++
						if len(acc.examples) < 3 {
							acc.examples = append(acc.examples, v.Evidence)
						}
						if acc.property == "" && o.Ontology != nil {
							acc.property = propertyForCue(o.Ontology, cue, v.Evidence)
						}
					}
				case domain.ViolationRedline, domain.ViolationRedlineFlag:
					f.Pack = v.Scope
					c.Redlines = append(c.Redlines, f)
				case domain.ViolationUngroundedFigure:
					continue // counted in metrics only, never a finding
				}
				flagged = true
			}
			switch {
			case s.WouldWithhold:
				c.WouldWithhold++
			case flagged || len(j.Violations) > 0:
				c.Flagged++
			}
		}
	}

	for cue, acc := range clusters {
		c.MissingFacts = append(c.MissingFacts, MissingFactCluster{
			Cue: cue, Property: acc.property, Conversations: len(acc.convs), Replies: acc.replies, Examples: acc.examples,
		})
	}
	sort.Slice(c.MissingFacts, func(i, j int) bool {
		a, b := c.MissingFacts[i], c.MissingFacts[j]
		if a.Conversations != b.Conversations {
			return a.Conversations > b.Conversations
		}
		return a.Cue < b.Cue
	})
	for _, mf := range unasserted {
		c.UnassertedProperties = append(c.UnassertedProperties, *mf)
	}
	sort.Slice(c.UnassertedProperties, func(i, j int) bool {
		return c.UnassertedProperties[i].Property < c.UnassertedProperties[j].Property
	})

	c.Sufficient = c.Replies >= c.MinSamples
	if c.Sufficient {
		rate := float64(c.WouldWithhold) / float64(c.Replies)
		c.WouldWithholdRate = &rate
	}
	if o.Ontology != nil && c.CustomerTurns >= c.MinSamples {
		c.SelfServe = &SelfServe{
			CustomerTurns: c.CustomerTurns, Answerable: answerable,
			Rate:  float64(answerable) / float64(c.CustomerTurns),
			Basis: "口径：客户问句提到了本体里已写下的事实属性，且不是查单、改地址一类的操作请求。这是上限估算，不是实测。",
		}
	}

	c.PackCases = domain.EvaluatePackCases(o.Packs)
	for _, id := range o.Packs {
		if p, ok := domain.RedlinePackByID(id); ok {
			c.PackReports = append(c.PackReports, PackReport{ID: p.ID, Label: p.Label, Rep: p.Report})
		}
	}
	return c
}

// cueGroups are the promise words a sentence may be clustered under, in
// order of preference: the selected packs' commitment types first (they are
// the industry's own names for its promises), then the built-in cues that
// name a promise (退款, 赔付, 质保...), and last the absolute words (全额,
// 免费) that mark a promise without naming one. Within a group the cue that
// sits nearest the unbacked word wins, so "赔付200元，全额退款" clusters its 200元
// under 赔付 — the thing promised — not under 全额, and "质保12个月，免费换新"
// clusters its 免费 under 换新 rather than under the 质保 that was backed.
func cueGroups(packs []string) [][]string {
	absolute := map[string]bool{}
	for _, w := range domain.AbsoluteWords() {
		absolute[w] = true
	}
	var named, abs []string
	for _, c := range domain.CommitmentCues() {
		if absolute[c] {
			abs = append(abs, c)
		} else {
			named = append(named, c)
		}
	}
	return [][]string{domain.CommitmentCuesFor(packs), named, abs}
}

// cueIn picks the cluster cue for a sentence: within the first group that
// has any, the cue nearest got (the unbacked word); when got is not in the
// sentence, the earliest cue. Empty when none applies.
func cueIn(sentence, got string, groups [][]string) string {
	anchor := -1
	if got != "" {
		anchor = strings.Index(sentence, got)
	}
	for _, group := range groups {
		best, bestDist, bestAt := "", -1, -1
		for _, cue := range group {
			i := strings.Index(sentence, cue)
			if i < 0 {
				continue
			}
			d := i
			if anchor >= 0 {
				d = distance(i, i+len(cue), anchor, anchor+len(got))
			}
			if best == "" || d < bestDist || (d == bestDist && (i < bestAt || (i == bestAt && len(cue) > len(best)))) {
				best, bestDist, bestAt = cue, d, i
			}
		}
		if best != "" {
			return best
		}
	}
	return ""
}

// distance is the gap in bytes between spans [a0,a1) and [b0,b1); zero when
// they touch or overlap.
func distance(a0, a1, b0, b1 int) int {
	switch {
	case a1 <= b0:
		return b0 - a1
	case b1 <= a0:
		return a0 - b1
	}
	return 0
}

// propertyForCue names the declared property a clustered promise is about:
// one the sentence mentions outright, or failing that one whose label
// contains the cue (质保 → 质保期). Only properties still without a value
// count: a promise left unbacked next to an asserted property is missing
// something else, and naming the asserted one would send the reader to fill
// in what is already there. Empty when nothing declared fits, which is
// itself the finding.
func propertyForCue(o *domain.Ontology, cue, sentence string) string {
	if props := o.MentionedUnassertedProperties(sentence); len(props) > 0 {
		return props[0]
	}
	for _, name := range o.PropertyNames() {
		if p := o.Properties[name]; p.Label != "" && strings.Contains(p.Label, cue) && !o.HasAssertion(name) {
			return name
		}
	}
	return ""
}
