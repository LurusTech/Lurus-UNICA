package judge

import (
	"fmt"
	"strings"

	"github.com/kefu/unica/pkg/domain"
	"github.com/kefu/unica/pkg/guardrail"
)

// Mode is the gate's posture as a standalone product: shadow records what it
// would have done and changes nothing; enforce withholds.
//
// Inside the router the same two postures are expressed per switch
// (domain.Config.Validation and .Commitment each carry off/shadow/enforce) so
// a line can enforce the firewall while claim checking is still shadow. The
// standalone gate exposes one knob because its users have not configured a
// product line and must not have to: the first contact is "plug it in and
// see", and the first upgrade is "turn it on". ModeConfig maps the knob onto
// the two switches.
type Mode string

const (
	ModeShadow  Mode = "shadow"
	ModeEnforce Mode = "enforce"
)

// ParseMode reads a mode; empty means shadow. An unknown value is an error
// rather than a default so a typo cannot leave an operator believing the gate
// is enforcing.
func ParseMode(s string) (Mode, error) {
	switch m := Mode(strings.ToLower(strings.TrimSpace(s))); m {
	case "":
		return ModeShadow, nil
	case ModeShadow, ModeEnforce:
		return m, nil
	default:
		return "", fmt.Errorf("unknown mode %q (want shadow or enforce)", s)
	}
}

// ModeConfig builds the ontology switches for a gate running in the given
// mode with the given redline packs. Facts are always injected when an
// ontology exists — the gate has no model to inject into, but FactsInjected is
// what lets a verified claim earn its confidence — and validation and the
// commitment firewall follow the mode together.
func ModeConfig(mode Mode, packs []string, scope domain.CommitmentScope) *domain.Config {
	cfg := domain.DefaultConfig()
	cfg.InjectFacts = true
	cfg.RedlinePacks = append([]string(nil), packs...)
	cfg.CommitmentScope = scope
	switch mode {
	case ModeEnforce:
		cfg.Validation = domain.ValidationEnforce
		cfg.Commitment = domain.CommitmentEnforce
	default:
		cfg.Validation = domain.ValidationShadow
		cfg.Commitment = domain.CommitmentShadow
	}
	return cfg
}

// EnforceTwin returns a copy of cfg with every shadow switch turned to enforce
// and everything else untouched. Nil stays nil: with no config there is
// nothing to enforce.
func EnforceTwin(cfg *domain.Config) *domain.Config {
	if cfg == nil {
		return nil
	}
	twin := *cfg
	if twin.Validation == domain.ValidationShadow {
		twin.Validation = domain.ValidationEnforce
	}
	if twin.Commitment == domain.CommitmentShadow {
		twin.Commitment = domain.CommitmentEnforce
	}
	return &twin
}

// MinShadowSamples is how many judged answers a shadow summary or a checkup
// needs before it states a rate. Below it, counts are given and the text
// says the sample is too small — never a percentage of eleven answers. One
// constant for the gate and the checkup so the two cannot disagree about
// when a number becomes a claim.
const MinShadowSamples = 30

// Shadowed is a judgement together with what enforcement would have done to
// the same answer.
type Shadowed struct {
	// Judgement is the verdict under the input's own configuration. Under
	// shadow it routes exactly as off would.
	Judgement Judgement
	// WouldWithhold reports that, had every shadow switch been enforce, the
	// answer would not have reached the customer because of its own content.
	// It is false whenever the input already enforces: there is no "would"
	// about an answer that was withheld.
	WouldWithhold bool
	// Enforced is the verdict the enforce twin produced, so a report can show
	// the reason and detail the agent would have received.
	Enforced Judgement
}

// JudgeShadowed judges once under the input's configuration and once under
// its enforce twin, without a breaker on either run so the two are
// independent of each other and of history. This is the whole of shadow
// mode's sales pitch — "this month the gate would have held N promises" — and
// it is computed, never estimated: the enforce run is the same pure function
// on the same input.
func JudgeShadowed(in Input, evaluator *guardrail.Evaluator) Shadowed {
	out := Shadowed{Judgement: Judge(in, evaluator, nil)}
	twin := in
	twin.OntologyCfg = EnforceTwin(in.OntologyCfg)
	out.Enforced = Judge(twin, evaluator, nil)
	out.WouldWithhold = out.Enforced.Withheld() && !out.Judgement.Withheld()
	return out
}
