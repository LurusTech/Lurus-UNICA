package replay

import "testing"

func TestCaseResult_ReducibleMajority_TiesCountAsReducible(t *testing.T) {
	tie := CaseResult{CaseID: "x", Trials: []Trial{
		{Escalated: true, Reason: "low_confidence"},
		{Escalated: false, Decision: "send"},
	}}
	if !tie.ReducibleMajority() {
		t.Error("a 1-1 tie should count as reducible")
	}

	minority := CaseResult{CaseID: "x", Trials: []Trial{
		{Escalated: true, Reason: "low_confidence"},
		{Escalated: false, Decision: "send"},
		{Escalated: false, Decision: "send"},
	}}
	if minority.ReducibleMajority() {
		t.Error("1 of 3 reducible trials is not a majority")
	}

	// A single unrepeated result behaves exactly like Class() == Reducible.
	single := CaseResult{CaseID: "x", Escalated: true, Reason: "low_confidence"}
	if !single.ReducibleMajority() {
		t.Error("an unrepeated reducible handoff must still count")
	}
}

func TestCaseResult_AllTrialsEscalated_RequiresEveryTrial(t *testing.T) {
	ok := CaseResult{Trials: []Trial{{Escalated: true}, {Escalated: true}}}
	if !ok.allTrialsEscalated() {
		t.Error("every trial escalating should satisfy pass^k")
	}

	oneAnswered := CaseResult{Trials: []Trial{{Escalated: true}, {Escalated: false}}}
	if oneAnswered.allTrialsEscalated() {
		t.Error("one trial answering instead of escalating must fail pass^k")
	}

	oneErrored := CaseResult{Trials: []Trial{{Escalated: true}, {Escalated: true, Err: "timeout"}}}
	if oneErrored.allTrialsEscalated() {
		t.Error("an errored trial must not count as escalated")
	}
}

// TestGate_ConfigMismatchFailsUnlessAllowed is the fingerprint half of the
// gate: two runs that routed under different settings are not comparable,
// whatever their scores say, unless the caller explicitly says to ignore it.
func TestGate_ConfigMismatchFailsUnlessAllowed(t *testing.T) {
	base := freshMart(t)
	cand := candidateWithAlias(t, base)
	baseFixed, candFixed := manyFixed(5)
	b := gateRun(t, base, baseFixed...)
	c := gateRun(t, cand, candFixed...)
	b.Fingerprint = Fingerprint{Guardrail: "g1", Validation: "shadow", Triage: "on", History: 2, Repeat: 1}
	c.Fingerprint = Fingerprint{Guardrail: "g1", Validation: "enforce", Triage: "on", History: 2, Repeat: 1}

	v, err := Gate(b, c)
	if err != nil {
		t.Fatal(err)
	}
	if v.Pass || len(v.ConfigMismatch) == 0 {
		t.Errorf("a validation-mode mismatch must fail the gate by default: %+v", v)
	}

	v2, err := GateWith(b, c, GateOptions{Alpha: 0.05, AllowConfigMismatch: true})
	if err != nil {
		t.Fatal(err)
	}
	if !v2.Pass || len(v2.ConfigMismatch) == 0 {
		t.Errorf("-allow-config-mismatch should pass while still reporting what differs: %+v", v2)
	}
}

// TestGate_MissingFingerprintOnlyWarns covers runs written before Fingerprint
// existed: the gate cannot check what it does not have, so it must warn, not
// fail.
func TestGate_MissingFingerprintOnlyWarns(t *testing.T) {
	base := freshMart(t)
	cand := candidateWithAlias(t, base)
	baseFixed, candFixed := manyFixed(5)
	b := gateRun(t, base, baseFixed...) // zero Fingerprint: an old file
	c := gateRun(t, cand, candFixed...)
	c.Fingerprint = Fingerprint{Guardrail: "g1", Validation: "shadow", Triage: "on", History: 2, Repeat: 1}

	v, err := Gate(b, c)
	if err != nil {
		t.Fatal(err)
	}
	if v.FingerprintWarning == "" {
		t.Error("a fingerprint missing on either side should be flagged as unverifiable")
	}
	if len(v.ConfigMismatch) != 0 {
		t.Error("with one side missing there is nothing to compare field by field")
	}
	if !v.Pass {
		t.Errorf("a missing fingerprint must not by itself fail the gate: %+v", v)
	}
}

// TestGate_NewViolationsCatchesSwappedViolation is the case totals alone
// cannot catch: the violating turn moves from one case to another, so
// ViolationsBefore and ViolationsAfter come out equal even though something
// got worse.
func TestGate_NewViolationsCatchesSwappedViolation(t *testing.T) {
	base := freshMart(t)
	cand := candidateWithAlias(t, base)
	baseFixed, candFixed := manyFixed(5)

	baseX := sent("x", "本店商品可以保修一年。") // violates under candOnt
	baseY := sent("y", "好的")          // clean
	candX := sent("x", "好的")          // now clean
	candY := sent("y", "本店商品可以保修一年。") // now violates: the swap

	b := gateRun(t, base, append(baseFixed, baseX, baseY)...)
	c := gateRun(t, cand, append(candFixed, candX, candY)...)
	v, err := Gate(b, c)
	if err != nil {
		t.Fatal(err)
	}
	if v.ViolationsBefore != v.ViolationsAfter {
		t.Fatalf("the totals must be equal for this to be a real test of the swap: before=%d after=%d",
			v.ViolationsBefore, v.ViolationsAfter)
	}
	if len(v.NewViolations) != 1 || v.NewViolations[0] != "y" {
		t.Errorf("NewViolations should name y, the turn the violation moved onto: %v", v.NewViolations)
	}
	if v.Pass {
		t.Error("a swapped violation must fail the gate even though the totals did not move")
	}
}

// TestGate_ForcedProtectionRequiresEveryRepeatedTrial is pass^k for the
// founding rule: one repeated trial that answers instead of escalating a
// forced turn is enough to lose the protection, however many other trials
// escalated.
func TestGate_ForcedProtectionRequiresEveryRepeatedTrial(t *testing.T) {
	base := freshMart(t)
	cand := candidateWithAlias(t, base)
	baseFixed, candFixed := manyFixed(5)

	baseForced := handedOff("b", "safety_incident")
	candPartial := CaseResult{
		CaseID: "b", Decision: "handoff", Reason: "safety_incident", Escalated: true,
		Trials: []Trial{
			{Escalated: true, Reason: "safety_incident"},
			{Escalated: false, Decision: "send", Answer: "好的"},
		},
	}

	b := gateRun(t, base, append(baseFixed, baseForced)...)
	c := gateRun(t, cand, append(candFixed, candPartial)...)
	v, err := Gate(b, c)
	if err != nil {
		t.Fatal(err)
	}
	if v.Pass || len(v.ForcedLost) != 1 || v.ForcedLost[0] != "b" {
		t.Errorf("one of two trials failing to escalate must lose forced protection: %+v", v)
	}
}

// TestGate_RepeatedTrialsUseMajorityForReducibility checks the symmetric half
// of repeat handling: a case whose trials split 2-1 for reducible on one side
// and 1-2 against on the other counts as a discordant pair, exactly like an
// unrepeated flip would.
func TestGate_RepeatedTrialsUseMajorityForReducibility(t *testing.T) {
	base := freshMart(t)
	cand := candidateWithAlias(t, base)
	baseFixed, candFixed := manyFixed(5)

	baseFlaky := CaseResult{CaseID: "flaky", Trials: []Trial{
		{Escalated: true, Reason: "low_confidence"},
		{Escalated: true, Reason: "low_confidence"},
		{Escalated: false, Decision: "send", Answer: "好的"},
	}}
	candFlaky := CaseResult{CaseID: "flaky", Trials: []Trial{
		{Escalated: false, Decision: "send", Answer: "好的"},
		{Escalated: false, Decision: "send", Answer: "好的"},
		{Escalated: true, Reason: "low_confidence"},
	}}

	b := gateRun(t, base, append(baseFixed, baseFlaky)...)
	c := gateRun(t, cand, append(candFixed, candFlaky)...)
	v, err := Gate(b, c)
	if err != nil {
		t.Fatal(err)
	}
	if v.Improved != 6 {
		t.Errorf("the majority vote should count 'flaky' as improved alongside the 5 clean fixes: improved=%d", v.Improved)
	}
}

// TestGate_ViolationOnANonFirstTrialIsNotInvisible guards the repeat feature's
// whole point: a candidate whose first sampled trial is clean must not get a
// free pass on a violation it produces on a later trial. Both the aggregate
// count and the swap-detecting NewViolations path have to see it.
func TestGate_ViolationOnANonFirstTrialIsNotInvisible(t *testing.T) {
	base := freshMart(t)
	cand := candidateWithAlias(t, base)
	baseFixed, candFixed := manyFixed(5)

	// Only the second of three trials affirms a denied claim; the first and
	// third are clean.
	baseX := sent("x", "好的")
	candX := CaseResult{
		CaseID: "x", Decision: "send", Answer: "好的",
		Trials: []Trial{
			{Decision: "send", Answer: "好的"},
			{Decision: "send", Answer: "本店商品可以保修一年。"},
			{Decision: "send", Answer: "好的"},
		},
	}

	b := gateRun(t, base, append(baseFixed, baseX)...)
	c := gateRun(t, cand, append(candFixed, candX)...)
	v, err := Gate(b, c)
	if err != nil {
		t.Fatal(err)
	}
	if v.ViolationsAfter == 0 {
		t.Error("a violation on the second of three trials must still be counted")
	}
	if len(v.NewViolations) != 1 || v.NewViolations[0] != "x" {
		t.Errorf("the swap check must catch a violation buried on a non-first trial: %v", v.NewViolations)
	}
	if v.Pass {
		t.Error("a candidate that ever affirms a denied claim must not pass, however few of its trials do")
	}
}

// TestVerdict_SmallSampleThreshold pins the Compared < 30 boundary the report
// uses to decide whether to print its "结论仅供参考" warning.
func TestVerdict_SmallSampleThreshold(t *testing.T) {
	base := freshMart(t)
	cand := candidateWithAlias(t, base)

	baseFixed, candFixed := manyFixed(29)
	b := gateRun(t, base, baseFixed...)
	c := gateRun(t, cand, candFixed...)
	v, err := Gate(b, c)
	if err != nil {
		t.Fatal(err)
	}
	if !v.SmallSample {
		t.Errorf("29 compared turns should be flagged small: %+v", v)
	}

	baseFixed, candFixed = manyFixed(30)
	b = gateRun(t, base, baseFixed...)
	c = gateRun(t, cand, candFixed...)
	v, err = Gate(b, c)
	if err != nil {
		t.Fatal(err)
	}
	if v.SmallSample {
		t.Errorf("30 compared turns should clear the small-sample threshold: %+v", v)
	}
}

func TestSummarise_DeliveredCleanPassK(t *testing.T) {
	results := []CaseResult{
		{CaseID: "a", Decision: "send", Trials: []Trial{
			{Decision: "send"}, {Decision: "send"}, {Decision: "send"},
		}},
		{CaseID: "b", Decision: "send", Trials: []Trial{
			{Decision: "send"}, {Escalated: true, Decision: "handoff", Reason: "low_confidence"}, {Decision: "send"},
		}},
	}
	s := Summarise(results)
	if s.DeliveredCleanPassK != 0.5 {
		t.Errorf("only 'a' delivered clean on every one of its trials: got %v", s.DeliveredCleanPassK)
	}
}

// TestGate_RecordsSplitAndDigestsOfWhatItJudged pins what evolve enqueue
// relies on: the verdict names the split and the exact candidate, and the
// digest is recomputed from the ontology rather than trusted from the file.
func TestGate_RecordsSplitAndDigestsOfWhatItJudged(t *testing.T) {
	base := freshMart(t)
	cand := candidateWithAlias(t, base)
	baseFixed, candFixed := manyFixed(5)
	b := gateRun(t, base, baseFixed...)
	c := gateRun(t, cand, candFixed...)
	b.Split, c.Split = SplitHoldout, SplitHoldout
	c.OntologyDigest = "forged"

	v, err := Gate(b, c)
	if err != nil {
		t.Fatal(err)
	}
	if v.Split != SplitHoldout {
		t.Errorf("split = %q, want holdout", v.Split)
	}
	if v.CandidateDigest != OntologyDigest(c.Ontology) || v.BaselineDigest != OntologyDigest(b.Ontology) {
		t.Errorf("digests must be recomputed from the judged ontologies: %+v", v)
	}
	if v.CandidateDigest == v.BaselineDigest {
		t.Error("an alias candidate must not share the baseline's digest")
	}

	b.Split = SplitTrain
	v, err = Gate(b, c)
	if err != nil {
		t.Fatal(err)
	}
	if v.Split != "" {
		t.Errorf("runs on different splits must not claim one: %q", v.Split)
	}
}
