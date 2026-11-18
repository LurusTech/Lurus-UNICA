package replay

import (
	"sort"
	"strings"

	"github.com/kefu/unica/pkg/domain"
)

func signatureSet(o *domain.Ontology, c Case) map[string]bool {
	set := map[string]bool{}
	set["reason:"+c.Logged.Reason] = true
	set["class:"+string(ClassifyReason(c.Logged.Reason))] = true
	for _, name := range o.MentionedProperties(c.Query) {
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

// clusterCases groups cases by single-link Jaccard similarity over their signatures,
// sorted deterministically by case ID.
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
