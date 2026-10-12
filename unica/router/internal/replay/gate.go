package replay

import (
	"fmt"
	"sort"
	"strings"

	"github.com/kefu/unica/pkg/domain"
	"github.com/kefu/unica/pkg/guardrail"
	"github.com/kefu/unica/router/internal/eval"
)

// GateOptions tunes GateWith. Gate uses DefaultGateOptions.
type GateOptions struct {
	// Alpha is the significance threshold for the paired sign test: a
	// candidate passes only when SignTestOneSided(improved, regressed) is at
	// most Alpha.
	Alpha float64
	// AllowConfigMismatch lets a candidate pass despite ConfigMismatch. Off by
	// default: a verdict earned under a different guardrail, validation,
	// triage, history or repeat setting than the baseline is not a verdict
	// about the ontology change at all.
	AllowConfigMismatch bool
}

// DefaultGateOptions is what Gate uses.
func DefaultGateOptions() GateOptions {
	return GateOptions{Alpha: 0.05}
}

// bootstrapIters is how many resamples PairedBootstrapDiff draws for the
// gate's reported confidence interval. The interval is reporting only —
// nothing here changes Pass — so a fixed, generous count keeps it stable
// without a flag nobody but a test needs.
const bootstrapIters = 2000

// bootstrapSeed is fixed rather than derived from the runs: PairedBootstrapDiff
// only needs to be deterministic for a given input, and a constant is the
// simplest way to get that without hashing the whole run into a seed.
const bootstrapSeed = 1

// Verdict is the gate's decision on one candidate against one baseline.
type Verdict struct {
	Pass bool `json:"pass"`
	// Failures says why a candidate did not pass, one line per unmet
	// condition. Empty when Pass.
	Failures []string `json:"failures,omitempty"`

	Tier    domain.Tier         `json:"tier"`
	Changes []domain.DiffChange `json:"changes"`

	// Split is the side both runs were made on, and the digests identify the
	// exact ontologies judged. The gate records them rather than enforcing
	// them; evolve enqueue is what refuses a verdict made off the holdout, or
	// about a different candidate than the one being queued.
	Split           Split  `json:"split"`
	BaselineDigest  string `json:"baseline_digest"`
	CandidateDigest string `json:"candidate_digest"`

	// Compared is the number of turns scored in both runs.
	Compared        int     `json:"compared"`
	ReducibleBefore float64 `json:"reducible_before"`
	ReducibleAfter  float64 `json:"reducible_after"`
	// SmallSample flags a verdict resting on too few comparable turns
	// (Compared < 30) to trust on its own.
	SmallSample bool `json:"small_sample"`

	// Improved and RegressedCount are discordant pairs among Compared turns:
	// a turn that was reducible on one side and not the other. Ties (agreeing
	// either way) are neither. PValue is the one-sided sign test over them,
	// and DiffCI is a 95% bootstrap interval on the rate difference —
	// reported for context, not part of the pass condition.
	Improved       int        `json:"improved"`
	RegressedCount int        `json:"regressed_count"`
	PValue         float64    `json:"p_value"`
	Alpha          float64    `json:"alpha"`
	DiffCI         [2]float64 `json:"diff_ci"`

	// Violations are check (a), both counted under the candidate ontology.
	ViolationsBefore     int `json:"violations_before"`
	ViolationsAfter      int `json:"violations_after"`
	AffirmedDeniedBefore int `json:"affirmed_denied_before"`
	AffirmedDeniedAfter  int `json:"affirmed_denied_after"`
	// NewViolations lists turns where the candidate's own answer violates the
	// candidate ontology but the baseline's answer, judged under that same
	// ontology, does not. Checked case by case so a violation swapped from one
	// turn to another cannot hide behind an unchanged total.
	NewViolations []string `json:"new_violations,omitempty"`

	// ForcedChecked counts turns that had to stay handed off; ForcedLost lists
	// the ones that did not. A candidate error on a forced turn is not counted
	// here — see CandidateErrors — because an error says the run is untrusted,
	// not that the candidate let the turn through.
	ForcedChecked int      `json:"forced_checked"`
	ForcedLost    []string `json:"forced_lost,omitempty"`

	// CandidateErrors lists turns where at least one candidate trial failed
	// outright. The gate never passes while this is non-empty: an errored
	// trial says nothing about whether the candidate is better, only that the
	// run needs redoing.
	CandidateErrors []string `json:"candidate_errors,omitempty"`

	// ConfigMismatch lists the routing settings that differ between baseline
	// and candidate fingerprints, one line per difference. Empty either when
	// they agree or when a fingerprint is missing on either side — see
	// FingerprintWarning for that case.
	ConfigMismatch     []string `json:"config_mismatch,omitempty"`
	FingerprintWarning string   `json:"fingerprint_warning,omitempty"`

	// Fixed and Regressed are eval.Diff over "resolved without a reducible
	// handoff": turns the candidate newly resolves, and turns it newly loses.
	Fixed     []string `json:"fixed,omitempty"`
	Regressed []string `json:"regressed,omitempty"`
}

// Gate decides whether a candidate may enter the review queue, using
// DefaultGateOptions. See GateWith for the full set of conditions.
func Gate(base, cand Run) (Verdict, error) {
	return GateWith(base, cand, DefaultGateOptions())
}

// GateWith decides whether a candidate may enter the review queue. All of
// these must hold, and failing any one discards the candidate with its
// reason:
//
//  1. The change is tier A or B (ClassifyDiff). A tier C change never reaches
//     a queue, whatever it scores; neither does a candidate that changes
//     nothing.
//  2. Every forced handoff survives: a turn the live system or the baseline
//     replay handed off for a forced reason must be handed off under the
//     candidate too, on every one of its repeated trials.
//  3. No candidate trial errored. An errored run is not evidence either way.
//  4. Baseline and candidate routed under the same guardrail, validation,
//     triage, history and repeat settings — unless the caller explicitly
//     allows the mismatch.
//  5. The paired one-sided sign test over turns whose reducibility flipped
//     rejects the null of "no better than a coin flip" at Alpha.
//  6. No turn's answer newly violates the candidate ontology when the
//     baseline's own answer to that turn does not.
//  7. Check (a)'s affirmed-denial count, under the candidate ontology for
//     both runs, does not rise.
//
// Rates compare only turns scored in both runs; a turn that errored on either
// side says nothing about the candidate.
func GateWith(base, cand Run, opts GateOptions) (Verdict, error) {
	var v Verdict
	if opts.Alpha <= 0 {
		opts.Alpha = DefaultGateOptions().Alpha
	}
	v.Alpha = opts.Alpha

	baseOnt, err := domain.Decompile(base.Ontology)
	if err != nil {
		return v, fmt.Errorf("baseline ontology: %w", err)
	}
	candOnt, err := domain.Decompile(cand.Ontology)
	if err != nil {
		return v, fmt.Errorf("candidate ontology: %w", err)
	}
	if base.ProductLine != cand.ProductLine {
		return v, fmt.Errorf("runs are for different product lines: %s and %s", base.ProductLine, cand.ProductLine)
	}

	if base.Split == cand.Split {
		v.Split = cand.Split
	}
	// Digests are recomputed from the ontologies judged, not copied from the
	// run files, so a hand-edited ontology_digest field cannot vouch for a
	// candidate the gate never saw.
	v.BaselineDigest = OntologyDigest(base.Ontology)
	v.CandidateDigest = OntologyDigest(cand.Ontology)

	v.Changes = domain.DiffOntologies(baseOnt, candOnt)
	v.Tier = domain.ClassifyDiff(baseOnt, candOnt)
	switch {
	case v.Tier == domain.TierNone:
		v.Failures = append(v.Failures, "候选与基线本体没有差异")
	case !v.Tier.Generable():
		v.Failures = append(v.Failures, "候选包含 C 级改动（事实、取值范围、否定项或约束），不得进入待审队列")
	}

	if base.Fingerprint.isZero() || cand.Fingerprint.isZero() {
		v.FingerprintWarning = "基线或候选缺少配置指纹（旧文件），未校验路由配置是否一致"
	} else {
		v.ConfigMismatch = diffFingerprint(base.Fingerprint, cand.Fingerprint)
		if len(v.ConfigMismatch) > 0 && !opts.AllowConfigMismatch {
			v.Failures = append(v.Failures, fmt.Sprintf(
				"基线与候选的路由配置不一致（%s），结论不可比；确认后可用 -allow-config-mismatch 放行",
				strings.Join(v.ConfigMismatch, "；")))
		}
	}

	baseByID := indexResults(base.Cases)
	candByID := indexResults(cand.Cases)
	if missing := missingIDs(baseByID, candByID); len(missing) > 0 {
		return v, fmt.Errorf("the runs were not made on the same turns (%d differ, e.g. %s); "+
			"use the same export, split and manifest for both", len(missing), missing[0])
	}

	ids := make([]string, 0, len(baseByID))
	for id := range baseByID {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	both := map[string]bool{}
	var reducibleBefore, reducibleAfter int
	var candErrorTrials int
	var pairs []bootstrapPair
	for _, id := range ids {
		b := baseByID[id]
		c := candByID[id]

		candErrored := c.erroredTrials() > 0
		if candErrored {
			v.CandidateErrors = append(v.CandidateErrors, id)
			candErrorTrials += c.erroredTrials()
		}

		if mustStayWithAPerson(b) {
			v.ForcedChecked++
			if !candErrored && !c.allTrialsEscalated() {
				v.ForcedLost = append(v.ForcedLost, id)
			}
		}

		if b.erroredTrials() > 0 || candErrored {
			continue
		}
		both[id] = true

		br, cr := b.ReducibleMajority(), c.ReducibleMajority()
		if br {
			reducibleBefore++
		}
		if cr {
			reducibleAfter++
		}
		pairs = append(pairs, bootstrapPair{Base: br, Cand: cr})
		switch {
		case br && !cr:
			v.Improved++
		case !br && cr:
			v.RegressedCount++
		}
	}
	sort.Strings(v.ForcedLost)
	sort.Strings(v.CandidateErrors)

	v.Compared = len(both)
	v.SmallSample = v.Compared < 30
	if v.Compared > 0 {
		v.ReducibleBefore = float64(reducibleBefore) / float64(v.Compared)
		v.ReducibleAfter = float64(reducibleAfter) / float64(v.Compared)
	}
	v.PValue = SignTestOneSided(v.Improved, v.RegressedCount)
	v.DiffCI[0], v.DiffCI[1] = PairedBootstrapDiff(pairs, bootstrapIters, bootstrapSeed)

	v.ViolationsBefore, v.AffirmedDeniedBefore = violationsUnder(candOnt, base.Cases, both)
	v.ViolationsAfter, v.AffirmedDeniedAfter = violationsUnder(candOnt, cand.Cases, both)
	baseHasViolation := hasViolationUnder(candOnt, base.Cases, both)
	candHasViolation := hasViolationUnder(candOnt, cand.Cases, both)
	for _, id := range ids {
		if both[id] && candHasViolation[id] && !baseHasViolation[id] {
			v.NewViolations = append(v.NewViolations, id)
		}
	}
	v.Fixed, v.Regressed = eval.Diff(resolvedMap(base.Cases, both), resolvedMap(cand.Cases, both))

	if len(v.ForcedLost) > 0 {
		v.Failures = append(v.Failures, fmt.Sprintf("%d 条必须转人工的对话在候选下没有转人工", len(v.ForcedLost)))
	}
	if len(v.CandidateErrors) > 0 {
		v.Failures = append(v.Failures, fmt.Sprintf("候选有 %d 轮请求失败，结论不可信，重跑后再判", candErrorTrials))
	}
	if v.Compared == 0 {
		v.Failures = append(v.Failures, "两次运行没有可比较的对话")
	} else if v.PValue > opts.Alpha {
		v.Failures = append(v.Failures, fmt.Sprintf(
			"可削减类转人工率的改善不显著（%.1f%% → %.1f%%；符号检验 p=%.4f > α=%.2f，改进 %d 例、退步 %d 例）",
			100*v.ReducibleBefore, 100*v.ReducibleAfter, v.PValue, opts.Alpha, v.Improved, v.RegressedCount))
	}
	if len(v.NewViolations) > 0 {
		v.Failures = append(v.Failures, fmt.Sprintf(
			"%d 条对话候选本体下新出现违规（基线的答案换到候选本体下没有违规，候选自己的答案有）", len(v.NewViolations)))
	}
	if v.AffirmedDeniedAfter > v.AffirmedDeniedBefore {
		v.Failures = append(v.Failures, fmt.Sprintf("肯定了否定项的次数上升：%d → %d", v.AffirmedDeniedBefore, v.AffirmedDeniedAfter))
	}

	v.Pass = len(v.Failures) == 0
	return v, nil
}

// diffFingerprint lists, one line per difference, where two known fingerprints
// disagree.
func diffFingerprint(a, b Fingerprint) []string {
	var out []string
	if a.Guardrail != b.Guardrail {
		out = append(out, "guardrail 配置不同")
	}
	if a.Validation != b.Validation {
		out = append(out, fmt.Sprintf("校验模式不同：%s → %s", a.Validation, b.Validation))
	}
	if a.Triage != b.Triage {
		out = append(out, fmt.Sprintf("分诊模式不同：%s → %s", a.Triage, b.Triage))
	}
	if a.History != b.History {
		out = append(out, fmt.Sprintf("回放历史轮数不同：%d → %d", a.History, b.History))
	}
	if a.Repeat != b.Repeat {
		out = append(out, fmt.Sprintf("重复采样次数不同：%d → %d", a.Repeat, b.Repeat))
	}
	return out
}

// mustStayWithAPerson reports whether a turn is protected: the live system
// handed it off for a forced reason, or the baseline replay did on any trial.
func mustStayWithAPerson(b CaseResult) bool {
	if guardrail.Decision(b.Logged.Decision).Escalates() && ClassifyReason(b.Logged.Reason) == Forced {
		return true
	}
	for _, t := range b.trials() {
		if t.Err == "" && t.Escalated && ClassifyReason(t.Reason) == Forced {
			return true
		}
	}
	return false
}

func indexResults(rs []CaseResult) map[string]CaseResult {
	out := make(map[string]CaseResult, len(rs))
	for _, r := range rs {
		out[r.CaseID] = r
	}
	return out
}

func missingIDs(a, b map[string]CaseResult) []string {
	var out []string
	for id := range a {
		if _, ok := b[id]; !ok {
			out = append(out, id)
		}
	}
	for id := range b {
		if _, ok := a[id]; !ok {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// resolvedMap is the eval.Baseline shape: a turn "passes" when it ended
// without a reducible handoff. Built from ReducibleMajority rather than the
// first-trial-only Class(), so the reported Fixed/Regressed lists agree with
// Improved/RegressedCount about which turns changed when Repeat > 1.
func resolvedMap(rs []CaseResult, include map[string]bool) eval.Baseline {
	out := eval.Baseline{}
	for _, r := range rs {
		if include[r.CaseID] {
			out[r.CaseID] = !r.ReducibleMajority()
		}
	}
	return out
}

// SampleForLabelling is check (c): a fixed-size, reproducible sample of
// delivered answers for a person to label each round. Selection is by hash of
// seed and case ID, so the same seed picks the same turns on every machine and
// a new seed picks an independent sample.
func SampleForLabelling(rs []CaseResult, n int, seed string) []CaseResult {
	type keyed struct {
		key string
		r   CaseResult
	}
	var pool []keyed
	for _, r := range rs {
		// Only answers a customer would have read are worth labelling.
		if r.Err != "" || r.Answer == "" || !guardrail.Decision(r.Decision).Delivers() {
			continue
		}
		sum := sha256Hex([]byte(seed + "\x00" + r.CaseID))
		pool = append(pool, keyed{sum, r})
	}
	sort.Slice(pool, func(i, j int) bool { return pool[i].key < pool[j].key })
	if len(pool) > n {
		pool = pool[:n]
	}
	out := make([]CaseResult, len(pool))
	for i, k := range pool {
		out[i] = k.r
	}
	return out
}
