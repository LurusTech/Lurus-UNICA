package domain

import (
	"regexp"
	"strings"
)

// Escalation tags are the third tag protocol, alongside [FACT:...] claims and
// [INTENT:...] marketing signals:
//
//	[HANDOFF:payout]
//	[HANDOFF:safety]
//
// The tag exists because of a defect the fresh-produce drill made impossible to
// ignore: asked about a child hospitalised after eating our fruit, the model
// produced a textbook answer that ended "我将立即为您转接人工专员" — and the
// guardrail routed nobody. The customer was promised a human by a system that
// had no way to hear the model say so. Keyword lists could not close that gap;
// the message contained none of them, and under TriageOn the keyword list is
// retired anyway.
//
// So the model states the escalation explicitly and the router acts on it. This
// also carries the intake protocol: the model collects the details a human needs
// before emitting the tag, which is why escalation must be a signal the model
// raises rather than a pre-dispatch interception. Intercepting before the model
// runs would leave the agent with an empty ticket, which is the situation the
// intake requirement exists to prevent.
const (
	// EscalatePayout is a request for money — a refund amount, compensation,
	// a return, freight reimbursement. The model may never decide these; it
	// collects the case details and raises this.
	EscalatePayout = "payout"
	// EscalateLiability is a liability question the model answered (who is at
	// fault, under which rule) and which still needs a human for the payout.
	EscalateLiability = "liability"
	// EscalateSafety is a food-safety or personal-injury incident. It skips
	// intake beyond the two minimum fields; delay here is itself the harm.
	EscalateSafety = "safety"
	// EscalateRegulator is a threat to involve a regulator or the press, or any
	// batch-scale complaint.
	EscalateRegulator = "regulator"
	// EscalateOther is what an unrecognised tag value becomes. An unknown value
	// still escalates: a model asking for a human and not getting one is the
	// defect this protocol exists to fix, so the vocabulary fails open.
	EscalateOther = "other"
)

var escalateTagPattern = regexp.MustCompile(`\[HANDOFF:([^\]]*)\]`)

// escalateTagStripPattern matches any [HANDOFF...] whatsoever, including the
// malformed shapes a model improvises — [HANDOFF] with no value, [HANDOFF:] with
// no reason. ParseEscalation strips by this wider pattern so a tag never reaches
// a customer just because it was written badly, exactly as the claim protocol
// does.
var escalateTagStripPattern = regexp.MustCompile(`\[HANDOFF[^\]]*\]`)

// EscalationResult is what ParseEscalation found.
type EscalationResult struct {
	// Requested reports that the model asked for a human.
	Requested bool
	// Reason is the normalised tag value, one of the Escalate* constants.
	Reason string
	// CleanedAnswer is the answer with every escalation tag removed.
	CleanedAnswer string
}

// knownEscalations maps a tag value to its normalised reason. Values are
// matched case-insensitively and with surrounding space trimmed, because a tag
// the model wrote as [HANDOFF: Payout ] means what it says.
var knownEscalations = map[string]string{
	EscalatePayout:    EscalatePayout,
	EscalateLiability: EscalateLiability,
	EscalateSafety:    EscalateSafety,
	EscalateRegulator: EscalateRegulator,
	EscalateOther:     EscalateOther,
}

// ParseEscalation extracts a [HANDOFF:...] tag from an answer and strips every
// escalation tag from the customer-facing text.
//
// The first well-formed tag wins. A model that emits two has contradicted
// itself, and picking the first keeps the outcome deterministic rather than
// dependent on which one happened to be last.
func ParseEscalation(answer string) EscalationResult {
	out := EscalationResult{CleanedAnswer: answer}

	for _, m := range escalateTagPattern.FindAllStringSubmatch(answer, -1) {
		value := strings.ToLower(strings.TrimSpace(m[1]))
		if value == "" {
			// [HANDOFF:] with no reason still means "get me a human".
			out.Requested, out.Reason = true, EscalateOther
			break
		}
		reason, known := knownEscalations[value]
		if !known {
			reason = EscalateOther
		}
		out.Requested, out.Reason = true, reason
		break
	}

	// A bare [HANDOFF] carries no colon and so never matches the capture
	// pattern, but it is still unambiguously a request.
	if !out.Requested && escalateTagStripPattern.MatchString(answer) {
		out.Requested, out.Reason = true, EscalateOther
	}

	out.CleanedAnswer = strings.TrimSpace(escalateTagStripPattern.ReplaceAllString(answer, ""))
	return out
}

// transferPhrases are the ways a model announces a handoff in prose. They are
// the safety net for a model that states the transfer but forgets the tag —
// the original D13 failure, where the promise reached the customer and the
// routing did not. Matching prose is a blunt instrument, so it is deliberately
// narrow: each phrase asserts a transfer is happening, not that one is possible.
var transferPhrases = []string{
	"为您转接人工",
	"为您转接专员",
	"转接人工客服",
	"转接人工专员",
	"已为您转接",
	"正在为您转接",
	"我将为您转接",
	"立即为您转接",

	// The model drops 接 as often as it keeps it — "我为您转人工核定" and
	// "转接人工客服" mean the same thing to a customer. A bare "转人工" is
	// deliberately absent: it also appears in answers describing when a
	// transfer would happen ("以下情形会转人工"), which is a policy
	// explanation, not a promise being made to this customer.
	"为您转人工",
	"帮您转人工",
	"转人工核定",
	"转人工处理",
	"转人工跟进",
}

// intakeMarkers show the model is still gathering the details a human will
// need. Their presence is what separates "转接中" from "补齐信息后转接" — the
// same transfer phrase means opposite things on either side of that line, and
// only one of them should route now.
var intakeMarkers = []string{
	"？", "?", "请提供", "请告知", "请问", "麻烦提供", "需要您提供",
	"方便提供", "以下信息", "确认几个",
}

// AnnouncesTransfer reports whether the answer tells the customer a human is
// taking over *now*. It exists so that promise can be made true even when the
// model omits the tag; the router treats it exactly as an escalation request.
//
// The intake guard is what makes the backstop usable. Without it the first
// intake turn escalates: the model correctly says "补齐以上信息后我会为您转接
// 人工核定", the phrase matches, and the conversation leaves for a human
// carrying exactly the empty ticket the intake protocol exists to prevent. A
// model that is still asking questions has not transferred anything, whatever
// it promised about later.
//
// This is not a substitute for the tag. A model that only ever announced in
// prose would escalate on phrasing, which drifts; the tag is the contract, is
// checked first, and is not subject to this guard — when the model tags a turn,
// it escalates even if it also asked something.
func AnnouncesTransfer(answer string) bool {
	for _, m := range intakeMarkers {
		if strings.Contains(answer, m) {
			return false
		}
	}
	for _, p := range transferPhrases {
		if strings.Contains(answer, p) {
			return true
		}
	}
	return false
}
