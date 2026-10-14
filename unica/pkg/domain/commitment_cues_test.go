package domain

import (
	"strings"
	"testing"
)

// 行业包 cue 含内置绝对词时，绝对词属性不能被长词吞掉。
func TestCueContainingAbsoluteWord(t *testing.T) {
	cues := []string{"免费试用"}
	answer := "本店提供免费试用"

	got := CheckCommitmentsWithCues(answer, nil, GroundingSources{}, CommitmentScopeCommitment, cues)
	if len(got) != 1 || got[0].Kind != ViolationUnbackedCommitment {
		t.Fatalf("带 cue 应拦截: %+v", got)
	}
	if !strings.Contains(got[0].Got, "免费") {
		t.Fatalf("Got 应含免费: %q", got[0].Got)
	}

	// 语料含「免费」时放行。
	src := GroundingSources{Retrieved: []string{"新用户可享免费服务"}}
	if got := CheckCommitmentsWithCues(answer, nil, src, CommitmentScopeCommitment, cues); len(got) != 0 {
		t.Fatalf("语料有出处应放行: %+v", got)
	}

	// 不带 cue：行为不变，且 Got 口径一致。
	base := CheckCommitmentsWithCues(answer, nil, GroundingSources{}, CommitmentScopeCommitment, nil)
	if len(base) != 1 || base[0].Got != got0(t, answer, cues) {
		t.Fatalf("口径应一致: %+v", base)
	}
}

func got0(t *testing.T, answer string, cues []string) string {
	t.Helper()
	g := CheckCommitmentsWithCues(answer, nil, GroundingSources{}, CommitmentScopeCommitment, cues)
	if len(g) != 1 {
		t.Fatalf("意外结果: %+v", g)
	}
	return g[0].Got
}
