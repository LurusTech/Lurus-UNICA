package main

import (
	"flag"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/kefu/unica/router/internal/eval"
	"github.com/kefu/unica/router/internal/replay"
)

// cmdCases is stage G of the evolution loop: turn real, already-escalated
// turns from an export into a draft regression-case file a person finishes
// by hand (replay.GoldenCandidates, goldencases.go). It writes no judgement
// of its own — intent and expect stay empty on every candidate — and it
// never touches testdata/golden itself; -out is a scratch file a person
// reviews, edits and moves into the golden corpus themselves.

// candidatesFile is the YAML shape written to -out: the same two top-level
// keys as a golden set file (see eval.GoldenSet), minus facts_of_record,
// which a candidate draft has no business asserting. cases.go deliberately
// does not reuse eval.GoldenSet here so an empty facts_of_record does not
// show up in every draft and get mistaken for something to fill in.
type candidatesFile struct {
	ProductLine string      `yaml:"product_line"`
	Cases       []eval.Case `yaml:"cases"`
}

// candidatesFileHeader explains, in the file itself, why a candidate draft
// cannot be dropped straight into testdata/golden: eval.LoadDir requires
// every case to declare an intent and to assert at least one expectation
// (router/internal/eval/goldenset.go, validateCase), so a file full of
// intentionally empty ones does not get skipped — LoadDir fails outright,
// and since it loads every *.yaml in the directory in one pass, dropping
// this file into testdata/golden as-is would break the entire golden corpus
// from loading, not just this file. Checked against validateCase directly,
// not assumed.
const candidatesFileHeader = `# 本文件由 evolve cases 从真实对话生成，是回归用例草稿，不是可直接使用的金标集文件。
#
# 每条 case 的字段对齐 unica/router/internal/eval/goldenset.go 里的 eval.Case：
# id / category / query / intent / expect / note。intent 和 expect 目前全部留空——
# 这不是可选项：eval 包的加载函数（LoadDir → validateCase）要求 intent 必填、
# expect 至少断言一项，否则整份文件都会加载失败并报错，不是跳过该条用例。
# LoadDir 一次性加载目录下所有 *.yaml，所以这份草稿如果原样放进
# testdata/golden/，会让整个金标集都加载不出来，不只是这一份文件。
#
# 用法：给每条 case 填好 intent，再按需要写 expect（must_contain_all /
# must_contain_any / must_not_contain / must_deny / must_not_match /
# handoff / escalate，见 goldenset.go 里 Expect 的定义），必要时再补 stage。
# note 里"坐席当时的回复"只是参考，不是标准答案——判断值必须由人填写，
# 事实、比例、价格、承诺、否定项都不能照抄现场原话直接当结论。
# 都填完之后，再把整条 case 移入对应产品线的 testdata/golden/*.yaml。
`

func cmdCases(args []string) error {
	fs := flag.NewFlagSet("cases", flag.ExitOnError)
	in := fs.String("in", "", "redacted export (evolve redact -out)")
	line := fs.String("line", "", "product line name")
	outPath := fs.String("out", "", "candidates.yaml to write")
	max := fs.Int("max", 50, "cap on how many candidates are written (0 = no cap)")
	fs.Parse(args)

	if *in == "" || *line == "" || *outPath == "" {
		return fmt.Errorf("-in, -line and -out are required")
	}
	if *max < 0 {
		*max = 0
	}

	convs, err := replay.LoadExport(*in)
	if err != nil {
		return err
	}
	var all []replay.Case
	for _, c := range replay.Cases(convs) {
		if c.ProductLine == *line {
			all = append(all, c)
		}
	}
	// Training side only, same discipline as evolve propose: a candidate
	// drafted from a holdout-side turn would let a case the gate is supposed
	// to judge on quietly become a development-side fixture instead.
	// -split is not a flag on this command on purpose.
	cases := replay.Select(all, replay.SplitTrain)
	if len(cases) == 0 {
		return fmt.Errorf("no training-side replayable turns for %s in %s", *line, *in)
	}

	candidates, err := replay.GoldenCandidates(cases, replay.GoldenCandidatesOptions{Max: *max})
	if err != nil {
		return err
	}
	if len(candidates) == 0 {
		return fmt.Errorf("%w: no escalated turn for %s in %s produced a candidate", errCheckFailed, *line, *in)
	}

	out := candidatesFile{ProductLine: *line, Cases: candidates}
	body, err := yaml.Marshal(out)
	if err != nil {
		return fmt.Errorf("marshal candidates: %w", err)
	}
	if err := os.WriteFile(*outPath, []byte(candidatesFileHeader+"\n"+string(body)), 0o600); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "共 %d 条候选，请逐条填写 expect 后再加入金标集：%s\n", len(candidates), *outPath)
	return nil
}
