// Package eval provides the golden-set scoring harness used to measure AI answer
// quality against known-correct product-line facts.
//
// The package is deliberately free of network and database dependencies so the
// assertion engine can be unit tested offline. Only cmd/evalset talks to Dify.
package eval

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Expected intent classes for a customer message. A case's intent is consumed
// offline by the intent classifier tests; it never requires a Dify call.
const (
	IntentInformational = "informational"
	IntentTransactional = "transactional"
	IntentEmotional     = "emotional"
)

// Expected commercial stages for a customer message, consumed offline by the
// stage classifier tests. Empty means "not annotated": stage labels are only
// added where a case is unambiguous, so the corpus never asserts a judgement
// call.
const (
	StagePresales  = "presales"
	StagePostsales = "postsales"
	StageUnknown   = "unknown"
)

// Expect holds the assertions applied to one answer.
//
// MustDeny is the closed-world assertion: the answer must not present the term
// as an available capability. See isAffirmed for its exact (and deliberately
// limited) semantics.
type Expect struct {
	MustContainAll []string `yaml:"must_contain_all"`
	MustContainAny []string `yaml:"must_contain_any"`
	MustNotContain []string `yaml:"must_not_contain"`
	MustDeny       []string `yaml:"must_deny"`
	MustNotMatch   []string `yaml:"must_not_match"`

	// Handoff is whether the answer was withheld from the customer. Nil means
	// "do not check", which lets a case assert only on answer content.
	Handoff *bool `yaml:"handoff"`

	// Escalate is whether a human was brought into the conversation. It is the
	// broader of the two: a suppressed answer escalates, and so does an answer
	// that is delivered alongside an escalation.
	//
	// The pair is what lets a case pin down the third routing outcome. Since the
	// AI was forbidden to decide refunds, the expected behaviour for most money
	// questions is an answer AND a human — `handoff: false, escalate: true` —
	// and content assertions still run, because that answer is exactly where the
	// model can promise a payout it is not allowed to promise.
	Escalate *bool `yaml:"escalate"`

	// compiled caches MustNotMatch patterns, populated during validation so a
	// malformed pattern fails at load time rather than mid-run.
	compiled []*regexp.Regexp
}

// Case is a single golden question with its expected answer properties.
type Case struct {
	ID       string `yaml:"id"`
	Category string `yaml:"category"`
	Query    string `yaml:"query"`
	Intent   string `yaml:"intent"`
	Stage    string `yaml:"stage,omitempty"`
	Expect   Expect `yaml:"expect"`
	Note     string `yaml:"note"`

	// ProductLine is back-filled from the owning set for convenience when cases
	// from several files are merged into one report.
	ProductLine string `yaml:"-"`
}

// GoldenSet is one product line's evaluation corpus.
type GoldenSet struct {
	ProductLine string `yaml:"product_line"`

	// FactsOfRecord mirrors the authoritative values this set was written
	// against. It is documentation for humans and the input to the phase-1
	// consistency test against deploy/config/canned_responses.yaml; the scorer
	// itself does not read it.
	FactsOfRecord map[string]interface{} `yaml:"facts_of_record"`

	Cases []Case `yaml:"cases"`
}

// LoadDir reads every *.yaml file in dir as a golden set, validates them, and
// returns them sorted by product line. IDs must be unique across all files.
func LoadDir(dir string) ([]GoldenSet, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, fmt.Errorf("glob %s: %w", dir, err)
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("no golden set files found in %s", dir)
	}
	sort.Strings(paths)

	seenIDs := make(map[string]string)
	sets := make([]GoldenSet, 0, len(paths))

	for _, path := range paths {
		set, err := loadFile(path)
		if err != nil {
			return nil, err
		}
		for i := range set.Cases {
			id := set.Cases[i].ID
			if prev, dup := seenIDs[id]; dup {
				return nil, fmt.Errorf("%s: duplicate case id %q (also in %s)", path, id, prev)
			}
			seenIDs[id] = path
		}
		sets = append(sets, set)
	}

	sort.Slice(sets, func(i, j int) bool { return sets[i].ProductLine < sets[j].ProductLine })
	return sets, nil
}

func loadFile(path string) (GoldenSet, error) {
	var set GoldenSet

	data, err := os.ReadFile(path)
	if err != nil {
		return set, fmt.Errorf("read %s: %w", path, err)
	}

	// KnownFields catches typos in assertion keys, which would otherwise be
	// silently ignored and quietly weaken the ruler.
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&set); err != nil {
		return set, fmt.Errorf("parse %s: %w", path, err)
	}

	if set.ProductLine == "" {
		return set, fmt.Errorf("%s: product_line is required", path)
	}
	if len(set.Cases) == 0 {
		return set, fmt.Errorf("%s: no cases defined", path)
	}

	for i := range set.Cases {
		set.Cases[i].ProductLine = set.ProductLine
		if err := validateCase(&set.Cases[i]); err != nil {
			return set, fmt.Errorf("%s: %w", path, err)
		}
	}
	return set, nil
}

func validateCase(c *Case) error {
	if c.ID == "" {
		return fmt.Errorf("case with query %q has no id", c.Query)
	}
	if strings.TrimSpace(c.Query) == "" {
		return fmt.Errorf("case %s has an empty query", c.ID)
	}

	switch c.Intent {
	case IntentInformational, IntentTransactional, IntentEmotional:
	case "":
		return fmt.Errorf("case %s: intent is required", c.ID)
	default:
		return fmt.Errorf("case %s: unknown intent %q", c.ID, c.Intent)
	}

	// Stage is optional: unannotated cases assert nothing about stage.
	switch c.Stage {
	case StagePresales, StagePostsales, StageUnknown, "":
	default:
		return fmt.Errorf("case %s: unknown stage %q", c.ID, c.Stage)
	}

	e := &c.Expect
	if len(e.MustContainAll) == 0 && len(e.MustContainAny) == 0 &&
		len(e.MustNotContain) == 0 && len(e.MustDeny) == 0 &&
		len(e.MustNotMatch) == 0 && e.Handoff == nil && e.Escalate == nil {
		return fmt.Errorf("case %s: expect block asserts nothing", c.ID)
	}

	e.compiled = make([]*regexp.Regexp, 0, len(e.MustNotMatch))
	for _, pattern := range e.MustNotMatch {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return fmt.Errorf("case %s: bad must_not_match pattern %q: %w", c.ID, pattern, err)
		}
		e.compiled = append(e.compiled, re)
	}
	return nil
}

// AllCases flattens the sets into a single slice, preserving set order.
func AllCases(sets []GoldenSet) []Case {
	var out []Case
	for _, s := range sets {
		out = append(out, s.Cases...)
	}
	return out
}
