package replay

import (
	"encoding/json"
	"math"
	"testing"
)

func TestSignTestOneSided_ExactValues(t *testing.T) {
	cases := []struct {
		improved, regressed int
		want                float64
	}{
		{0, 0, 1},             // no discordant pairs at all: nothing to reject on
		{5, 0, 1.0 / 32.0},    // clean sweep of 5, the gate's own worked example
		{1, 0, 0.5},           // a single flip is exactly a coin flip
		{3, 2, 0.5},           // C(5,3)+C(5,4)+C(5,5) = 10+5+1 = 16, /32 = 0.5
		{10, 0, 1.0 / 1024.0}, // 2^10
	}
	for _, c := range cases {
		got := SignTestOneSided(c.improved, c.regressed)
		if math.Abs(got-c.want) > 1e-9 {
			t.Errorf("SignTestOneSided(%d, %d) = %v, want %v", c.improved, c.regressed, got, c.want)
		}
	}
}

// TestSignTestOneSided_OneVsZeroDoesNotClearDefaultAlpha pins the gate's own
// example: a single improved case against zero regressed is not, on its own,
// evidence at the default alpha of 0.05.
func TestSignTestOneSided_OneVsZeroDoesNotClearDefaultAlpha(t *testing.T) {
	p := SignTestOneSided(1, 0)
	if p != 0.5 {
		t.Fatalf("SignTestOneSided(1, 0) = %v, want 0.5", p)
	}
	if p <= DefaultGateOptions().Alpha {
		t.Errorf("1:0 must not pass the gate's default alpha: p=%v", p)
	}
}

func TestSignTestOneSided_LargeNDoesNotUnderflow(t *testing.T) {
	// float64(0.5)^1000 underflows to 0 well before n=1074; the exact
	// big.Int-backed tail must not.
	p := SignTestOneSided(520, 480)
	if p <= 0 || p > 1 {
		t.Fatalf("SignTestOneSided(520, 480) = %v, out of range", p)
	}
	if p >= 0.5 {
		t.Errorf("520 vs 480 out of 1000 should be a weak signal, not none at all: p=%v", p)
	}
}

func TestPassHatK(t *testing.T) {
	cases := []struct {
		c, n, k int
		want    float64
	}{
		{0, 0, 0, 1}, // k=0: the empty subset always "passes"
		{3, 5, 0, 1},
		{5, 5, 5, 1},         // every trial succeeded: certain
		{4, 5, 5, 0},         // one failure and k asks for all 5: impossible
		{2, 4, 2, 1.0 / 6.0}, // C(2,2)/C(4,2) = 1/6
		{4, 4, 2, 1},         // all 4 succeeded, any pair of them succeeds
	}
	for _, c := range cases {
		got := PassHatK(c.c, c.n, c.k)
		if math.Abs(got-c.want) > 1e-9 {
			t.Errorf("PassHatK(%d, %d, %d) = %v, want %v", c.c, c.n, c.k, got, c.want)
		}
	}
}

func TestPairedBootstrapDiff_DeterministicForTheSameSeed(t *testing.T) {
	pairs := []bootstrapPair{
		{Base: true, Cand: false},
		{Base: true, Cand: false},
		{Base: false, Cand: false},
		{Base: true, Cand: true},
		{Base: false, Cand: true},
	}
	lo1, hi1 := PairedBootstrapDiff(pairs, 500, 42)
	lo2, hi2 := PairedBootstrapDiff(pairs, 500, 42)
	if lo1 != lo2 || hi1 != hi2 {
		t.Fatalf("same seed gave different intervals: (%v,%v) vs (%v,%v)", lo1, hi1, lo2, hi2)
	}
	if lo1 > hi1 {
		t.Errorf("lo > hi: %v > %v", lo1, hi1)
	}

	lo3, hi3 := PairedBootstrapDiff(pairs, 500, 7)
	if lo1 == lo3 && hi1 == hi3 {
		t.Error("a different seed should not reliably reproduce the exact same interval")
	}
}

func TestPairedBootstrapDiff_EmptyIsZero(t *testing.T) {
	lo, hi := PairedBootstrapDiff(nil, 500, 1)
	if lo != 0 || hi != 0 {
		t.Errorf("empty pairs should report (0, 0), got (%v, %v)", lo, hi)
	}
}

// TestOntologyDigest_IgnoresIndentation pins the property enqueue depends on:
// a run file stores the compiled ontology indented, and it must still hash
// the same as the compact compile propose made.
func TestOntologyDigest_IgnoresIndentation(t *testing.T) {
	compact := json.RawMessage(`{"product_line":"X","properties":{"a":{"label":"甲"}}}`)
	indented := json.RawMessage("{\n  \"product_line\": \"X\",\n  \"properties\": {\n    \"a\": {\"label\": \"甲\"}\n  }\n}")
	if OntologyDigest(compact) != OntologyDigest(indented) {
		t.Error("indentation changed the digest")
	}
	other := json.RawMessage(`{"product_line":"X","properties":{"a":{"label":"乙"}}}`)
	if OntologyDigest(compact) == OntologyDigest(other) {
		t.Error("a different ontology hashed the same")
	}
}
