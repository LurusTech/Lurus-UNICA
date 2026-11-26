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

// Short industry cues found inside other words, and cues denied by a
// directly preceding 不 or 无, are not promises: nothing below may be held.
// (Their figures are still counted as ungrounded, which holds nothing.)
func TestCommitmentCues_NotPromises(t *testing.T) {
	cues := CommitmentCuesFor([]string{"ecommerce"})
	for _, s := range []string{
		"我们的商品品质保障，支持7天无理由退货。",
		"我们确保价格与官网一致，现价199元。",
		"人为损坏不保修，维修费200元。",
		"此款无质保，售价88元。",
		"这项服务不免费，收费30元。",
	} {
		for _, f := range CheckCommitmentsWithCues(s, nil, GroundingSources{}, CommitmentScopeCommitment, cues) {
			if IsBlockingFinding(f.Kind) {
				t.Errorf("%s: blocking finding %+v, want none", s, f)
			}
		}
	}
}

// The same cues still catch the promise they exist for — including where a
// word next to them looks like an exclusion or a denial but is not one: 产品质保
// is the warranty itself, a denied 包邮 leaves the refund after it promised, and
// 不得不 commits.
func TestCommitmentCues_StillCatchPromises(t *testing.T) {
	cues := CommitmentCuesFor([]string{"ecommerce"})
	for _, s := range []string{
		"本商品保修24个月。",
		"下单后保价30天，降价补差价。",
		"质保两年，坏了包换，换新只需3天。",
		"本产品质保2年。",
		"该商品质保3年。",
		"我们明确保修3年。",
		"本品不包邮，全额退款。",
		"无质保，全额退款。",
		"这款不包邮，但全额退款。",
		"不得不全额退款。",
	} {
		blocking := false
		for _, f := range CheckCommitmentsWithCues(s, nil, GroundingSources{}, CommitmentScopeCommitment, cues) {
			blocking = blocking || IsBlockingFinding(f.Kind)
		}
		if !blocking {
			t.Errorf("%s: no blocking finding, want an unbacked commitment", s)
		}
	}
}
