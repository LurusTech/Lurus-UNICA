package replay

import (
	"math/big"
	"sort"
)

// SignTestOneSided is the exact one-sided paired sign test the gate uses to
// decide whether a candidate's fall in reducible handoffs is more than noise.
//
// improved and regressed count only the discordant pairs — cases that flipped
// one way or the other between baseline and candidate; a case both sides agree
// on says nothing about which is better and is not passed in here. Under the
// null that the candidate is no better than a coin flip, each discordant pair
// is a fair coin, so the p-value is the upper tail of Binomial(n, 0.5):
//
//	P(X >= improved | n = improved+regressed, p = 0.5)
//
// Computed with math/big rather than float64 powers of two, which underflow
// to zero long before a few hundred paired turns.
func SignTestOneSided(improved, regressed int) float64 {
	n := improved + regressed
	if n <= 0 {
		return 1
	}
	if improved < 0 {
		improved = 0
	}
	if improved > n {
		return 0
	}

	tail := new(big.Int)
	for k := improved; k <= n; k++ {
		tail.Add(tail, new(big.Int).Binomial(int64(n), int64(k)))
	}
	num := new(big.Float).SetPrec(128).SetInt(tail)
	den := new(big.Float).SetPrec(128).SetMantExp(big.NewFloat(1), n) // 2^n
	p, _ := new(big.Float).Quo(num, den).Float64()
	return p
}

// PassHatK is τ-bench's unbiased pass^k estimator: given n independent trials
// of the same case, c of which succeeded, the estimated probability that all k
// of a random k-subset of those trials succeed is C(c,k)/C(n,k).
//
// Computed as a telescoping product of falling factorials rather than binomial
// coefficients directly, so it never needs numbers larger than c or n and never
// touches math/big for what is, in practice, a handful of repeats.
func PassHatK(c, n, k int) float64 {
	if k <= 0 {
		return 1
	}
	if n < k || c < k {
		return 0
	}
	result := 1.0
	for i := 0; i < k; i++ {
		result *= float64(c-i) / float64(n-i)
	}
	return result
}

// bootstrapPair is one case's paired outcome: whether it was reducible under
// the baseline and under the candidate. PairedBootstrapDiff's signature spells
// this out as an anonymous struct because it is the type signature-mining code
// is expected to build against.
type bootstrapPair = struct{ Base, Cand bool }

// PairedBootstrapDiff estimates a 95% interval, by the percentile method, for
// "candidate reducible rate minus baseline reducible rate" over the compared
// cases. It resamples whole pairs with replacement — a case's baseline and
// candidate outcomes always travel together — iters times, using a
// deterministic PRNG seeded by the caller so the same input always produces
// the same interval.
//
// This is reporting only: nothing here feeds the pass/fail decision, which
// rests on the exact sign test above.
func PairedBootstrapDiff(pairs []bootstrapPair, iters int, seed uint64) (lo, hi float64) {
	n := len(pairs)
	if n == 0 || iters <= 0 {
		return 0, 0
	}

	rng := newSplitMix64(seed)
	diffs := make([]float64, iters)
	for i := 0; i < iters; i++ {
		var baseCount, candCount int
		for j := 0; j < n; j++ {
			idx := int(rng.next() % uint64(n))
			p := pairs[idx]
			if p.Base {
				baseCount++
			}
			if p.Cand {
				candCount++
			}
		}
		diffs[i] = float64(candCount-baseCount) / float64(n)
	}
	sort.Float64s(diffs)
	return percentile(diffs, 0.025), percentile(diffs, 0.975)
}

// percentile linearly interpolates the p-th percentile (0..1) of a sorted
// slice.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	pos := p * float64(len(sorted)-1)
	lo := int(pos)
	hi := lo + 1
	if hi >= len(sorted) {
		return sorted[len(sorted)-1]
	}
	frac := pos - float64(lo)
	return sorted[lo]*(1-frac) + sorted[hi]*frac
}

// splitMix64 is a small, deterministic, dependency-free PRNG. math/rand's
// global state is a shared resource a concurrent replay run should not touch;
// this one is seeded and owned entirely by one call to PairedBootstrapDiff.
type splitMix64 struct{ state uint64 }

func newSplitMix64(seed uint64) *splitMix64 { return &splitMix64{state: seed} }

func (s *splitMix64) next() uint64 {
	s.state += 0x9E3779B97F4A7C15
	z := s.state
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}
